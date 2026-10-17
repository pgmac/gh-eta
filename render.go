package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// fmtDur renders a duration the way people read CI times: 45s, 7m36s, 1h02m.
func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// summary is the one-line verdict for a run.
func summary(e RunEstimate) string {
	if e.Run.Status == "completed" {
		return "finished: " + e.Run.Conclusion
	}
	if e.HistoryRuns == 0 {
		return "no successful past runs to compare with"
	}
	var parts []string
	if e.Blocked {
		parts = append(parts, "BLOCKED: waiting for approval")
		if e.Remaining > 0 {
			parts = append(parts, "~"+fmtDur(e.Remaining)+" once approved")
		}
	} else {
		parts = append(parts, "~"+fmtDur(e.Remaining)+" remaining",
			"ETA "+e.ETA.Local().Format("15:04:05"))
	}
	if e.Partial {
		parts = append(parts, "some jobs have no history")
	}
	return strings.Join(parts, " · ")
}

func jobRemaining(je JobEstimate) string {
	switch je.State {
	case StateDone:
		return "-"
	case StateUnknown:
		return "?"
	case StateOverdue:
		return "overdue"
	case StateBlocked:
		return "blocked (+" + fmtDur(je.Remaining) + ")"
	}
	s := fmtDur(je.Remaining)
	if je.Basis == "steps" {
		s += " (steps)"
	}
	return s
}

func jobStatus(je JobEstimate) string {
	if je.Status == "completed" {
		return je.Conclusion
	}
	return je.Status
}

// renderText writes the human-readable report for each run.
func renderText(w io.Writer, ests []RunEstimate) error {
	for i, e := range ests {
		if i > 0 {
			fmt.Fprintln(w)
		}
		r := e.Run
		ref := r.Branch
		if r.Event != "" {
			ref += " (" + r.Event + ")"
		}
		fmt.Fprintf(w, "%s · %s · run %d · %s\n", r.Repo, r.WorkflowName, r.ID, ref)
		if r.Title != "" && r.Title != r.WorkflowName {
			fmt.Fprintf(w, "%s\n", r.Title)
		}
		if r.URL != "" {
			fmt.Fprintln(w, r.URL)
		}
		fmt.Fprintf(w, "elapsed %s · %s\n", fmtDur(e.Elapsed), summary(e))
		fmt.Fprintf(w, "history: %d successful runs\n\n", e.HistoryRuns)

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "JOB\tSTATUS\tELAPSED\tP50\tP90\tREMAINING")
		for _, je := range e.Jobs {
			elapsed, p50, p90 := "-", "-", "-"
			if je.Elapsed > 0 {
				elapsed = fmtDur(je.Elapsed)
			}
			if je.Typical != nil {
				p50, p90 = fmtDur(je.Typical.P50), fmtDur(je.Typical.P90)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", je.Name, jobStatus(je), elapsed, p50, p90, jobRemaining(je))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

type jsonJob struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Conclusion string   `json:"conclusion,omitempty"`
	State      string   `json:"state"`
	Elapsed    float64  `json:"elapsedSeconds"`
	P50        *float64 `json:"p50Seconds,omitempty"`
	P90        *float64 `json:"p90Seconds,omitempty"`
	Samples    int      `json:"samples"`
	Remaining  float64  `json:"remainingSeconds"`
	Basis      string   `json:"basis,omitempty"`
}

type jsonRun struct {
	Repo        string    `json:"repo"`
	RunID       int64     `json:"runId"`
	Workflow    string    `json:"workflow"`
	Title       string    `json:"title"`
	Branch      string    `json:"branch"`
	Event       string    `json:"event"`
	Status      string    `json:"status"`
	URL         string    `json:"url"`
	HistoryRuns int       `json:"historyRuns"`
	Elapsed     float64   `json:"elapsedSeconds"`
	Remaining   float64   `json:"remainingSeconds"`
	ETA         time.Time `json:"eta"`
	Blocked     bool      `json:"blocked"`
	Partial     bool      `json:"partial"`
	Jobs        []jsonJob `json:"jobs"`
}

func secs(d time.Duration) float64 { return d.Round(time.Second).Seconds() }

// renderJSON writes all estimates as one JSON array.
func renderJSON(w io.Writer, ests []RunEstimate) error {
	out := make([]jsonRun, 0, len(ests))
	for _, e := range ests {
		r := e.Run
		jr := jsonRun{
			Repo: r.Repo, RunID: r.ID, Workflow: r.WorkflowName, Title: r.Title,
			Branch: r.Branch, Event: r.Event, Status: r.Status, URL: r.URL,
			HistoryRuns: e.HistoryRuns, Elapsed: secs(e.Elapsed), Remaining: secs(e.Remaining),
			ETA: e.ETA.UTC(), Blocked: e.Blocked, Partial: e.Partial, Jobs: []jsonJob{},
		}
		for _, je := range e.Jobs {
			jj := jsonJob{
				Name: je.Name, Status: je.Status, Conclusion: je.Conclusion, State: je.State,
				Elapsed: secs(je.Elapsed), Remaining: secs(je.Remaining), Basis: je.Basis,
			}
			if je.Typical != nil {
				p50, p90 := secs(je.Typical.P50), secs(je.Typical.P90)
				jj.P50, jj.P90, jj.Samples = &p50, &p90, je.Typical.N
			}
			jr.Jobs = append(jr.Jobs, jj)
		}
		out = append(out, jr)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
