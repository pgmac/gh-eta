// Command gh-eta is a gh extension that estimates how long running GitHub
// Actions workflow runs have left, from the history of the same workflow.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/cli/go-gh/v2/pkg/term"
	"github.com/spf13/pflag"
)

// version is set at build time by the release workflow.
var version = "dev"

// buildVersion is version, or failing that what `go install` recorded.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

const usage = `Estimate time remaining for running GitHub Actions workflow runs.

Usage:
  gh eta [<run-id>] [flags]

With no run ID, every active (queued, waiting or in progress) run in the
repository is estimated. Estimates come from the per-job and per-step
durations of recent successful runs of the same workflow.

Flags:
`

type options struct {
	repo       string
	owners     []string
	history    int
	sameBranch bool
	anyEvent   bool
	watch      bool
	interval   time.Duration
	json       bool
	version    bool
	runID      int64
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "gh eta:", err)
		os.Exit(1)
	}
}

func parseArgs(args []string, errOut io.Writer) (options, error) {
	var o options
	fs := pflag.NewFlagSet("gh eta", pflag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Usage = func() {
		fmt.Fprint(errOut, usage)
		fs.PrintDefaults()
	}
	fs.StringVarP(&o.repo, "repo", "R", "", "repository as `OWNER/REPO` (default: current repo)")
	fs.StringSliceVar(&o.owners, "owner", nil, "scan every repo of these users or orgs for active runs")
	fs.IntVarP(&o.history, "history", "n", 10, "number of past successful runs to learn from")
	fs.BoolVar(&o.sameBranch, "same-branch", false, "only learn from runs on the same branch")
	fs.BoolVar(&o.anyEvent, "any-event", false, "learn from runs of any trigger event, not just the same one")
	fs.BoolVarP(&o.watch, "watch", "w", false, "refresh until the runs finish")
	fs.DurationVarP(&o.interval, "interval", "i", 15*time.Second, "refresh interval for --watch")
	fs.BoolVar(&o.json, "json", false, "print estimates as JSON")
	fs.BoolVar(&o.version, "version", false, "print version")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch fs.NArg() {
	case 0:
	case 1:
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil || id <= 0 {
			return o, fmt.Errorf("invalid run ID %q", fs.Arg(0))
		}
		o.runID = id
	default:
		return o, errors.New("at most one run ID")
	}
	if o.runID != 0 && len(o.owners) > 0 {
		return o, errors.New("--owner cannot be used with a run ID")
	}
	if o.repo != "" && len(o.owners) > 0 {
		return o, errors.New("--owner cannot be used with --repo")
	}
	if o.history < 1 || o.history > 100 {
		return o, errors.New("--history must be between 1 and 100")
	}
	if o.interval < 5*time.Second {
		return o, errors.New("--interval must be at least 5s")
	}
	return o, nil
}

// estimator fetches and estimates runs, keeping each workflow's history for
// the life of the process: it does not change while a run is in flight.
type estimator struct {
	c       API
	o       options
	history map[string]History
}

func (e *estimator) historyFor(r Run) (History, error) {
	q := HistoryQuery{Repo: r.Repo, WorkflowID: r.WorkflowID, Limit: e.o.history, Exclude: r.ID}
	if !e.o.anyEvent {
		q.Event = r.Event
	}
	if e.o.sameBranch {
		q.Branch = r.Branch
	}
	if h, ok := e.history[q.key()]; ok {
		return h, nil
	}
	runs, err := fetchHistory(e.c, q)
	if err != nil {
		return History{}, fmt.Errorf("history for %s: %w", r.WorkflowName, err)
	}
	h := BuildHistory(runs)
	e.history[q.key()] = h
	return h, nil
}

func (e *estimator) runs(repos []string) ([]Run, error) {
	if e.o.runID != 0 {
		r, err := fetchRun(e.c, repos[0], e.o.runID)
		if err != nil {
			return nil, err
		}
		return []Run{r}, nil
	}
	if len(repos) == 1 {
		return activeRuns(e.c, repos[0])
	}
	return activeRunsIn(e.c, repos), nil
}

func (e *estimator) estimates(repos []string) ([]RunEstimate, error) {
	runs, err := e.runs(repos)
	if err != nil {
		return nil, err
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Repo != runs[j].Repo {
			return runs[i].Repo < runs[j].Repo
		}
		return runs[i].ID < runs[j].ID
	})
	now := time.Now()
	ests := make([]RunEstimate, 0, len(runs))
	for _, r := range runs {
		h, err := e.historyFor(r)
		if err != nil {
			return nil, err
		}
		ests = append(ests, Estimate(r, h, now))
	}
	return ests, nil
}

func resolveRepos(c API, o options) ([]string, error) {
	if len(o.owners) > 0 {
		var all []string
		for _, owner := range o.owners {
			rs, err := ownerRepos(c, owner)
			if err != nil {
				return nil, fmt.Errorf("repos of %s: %w", owner, err)
			}
			all = append(all, rs...)
		}
		return all, nil
	}
	if o.repo != "" {
		r, err := repository.Parse(o.repo)
		if err != nil {
			return nil, err
		}
		return []string{r.Owner + "/" + r.Name}, nil
	}
	r, err := repository.Current()
	if err != nil {
		return nil, fmt.Errorf("not in a GitHub repository (use -R OWNER/REPO): %w", err)
	}
	return []string{r.Owner + "/" + r.Name}, nil
}

func allDone(ests []RunEstimate) bool {
	for _, e := range ests {
		if e.Run.Status != "completed" {
			return false
		}
	}
	return true
}

func run(args []string, out io.Writer) error {
	o, err := parseArgs(args, os.Stderr)
	if err != nil {
		return err
	}
	if o.version {
		fmt.Fprintln(out, "gh-eta", buildVersion())
		return nil
	}
	c, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	repos, err := resolveRepos(c, o)
	if err != nil {
		return err
	}
	e := &estimator{c: c, o: o, history: map[string]History{}}
	tty := term.FromEnv().IsTerminalOutput()

	for {
		ests, err := e.estimates(repos)
		if err != nil {
			return err
		}
		if o.watch && tty && !o.json {
			fmt.Fprint(out, "\033[H\033[2J")
		}
		switch {
		case o.json:
			err = renderJSON(out, ests)
		case len(ests) == 0:
			where := repos[0]
			if len(repos) > 1 {
				where = fmt.Sprintf("%d repos", len(repos))
			}
			fmt.Fprintf(out, "no active runs in %s\n", where)
		default:
			err = renderText(out, ests)
		}
		if err != nil {
			return err
		}
		if !o.watch || allDone(ests) {
			return nil
		}
		time.Sleep(o.interval)
	}
}
