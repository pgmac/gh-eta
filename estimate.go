package main

import (
	"math"
	"sort"
	"time"
)

// Step is one step of a workflow job, as returned by the jobs API.
type Step struct {
	Name        string    `json:"name"`
	Number      int       `json:"number"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// Job is one job of a workflow run.
type Job struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Steps       []Step    `json:"steps"`
}

// Run is a workflow run plus its jobs.
type Run struct {
	ID           int64     `json:"id"`
	Repo         string    `json:"-"`
	WorkflowID   int64     `json:"workflow_id"`
	WorkflowName string    `json:"name"`
	Title        string    `json:"display_title"`
	Event        string    `json:"event"`
	Branch       string    `json:"head_branch"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	URL          string    `json:"html_url"`
	Jobs         []Job     `json:"-"`
}

// Stats summarises a set of observed durations.
type Stats struct {
	P50 time.Duration
	P90 time.Duration
	N   int
}

// JobHistory is what past runs say about one job, keyed by job name.
type JobHistory struct {
	Duration Stats
	// Gap is the delay between the job's predecessors finishing (or the run
	// starting, for jobs with no predecessors) and the job starting. It covers
	// runner pickup and, for gated jobs, the usual approval wait.
	Gap   Stats
	Steps map[string]Stats
	// Preds are jobs that finished before this one started in every past run
	// where both ran. They stand in for the workflow's `needs` graph.
	Preds map[string]bool
}

// History is the per-job summary of a set of successful past runs.
type History struct {
	Runs int
	Jobs map[string]*JobHistory
}

// Job states reported in estimates.
const (
	StateDone    = "done"
	StateRunning = "running"
	StateOverdue = "overdue"
	StatePending = "pending"
	StateBlocked = "blocked"
	StateUnknown = "unknown"
)

// JobEstimate is the forecast for one job of the run being estimated.
type JobEstimate struct {
	Name       string
	Status     string
	Conclusion string
	State      string
	Elapsed    time.Duration
	Typical    *Stats
	Remaining  time.Duration
	// Basis says how Remaining was worked out: "steps", "job" or "" when it
	// was not (done, unknown).
	Basis string
}

// RunEstimate is the forecast for a whole run.
type RunEstimate struct {
	Run         Run
	Now         time.Time
	HistoryRuns int
	Elapsed     time.Duration
	Remaining   time.Duration
	ETA         time.Time
	// Blocked is true when a job is waiting on an environment approval. Then
	// Remaining and ETA assume the approval is given now.
	Blocked bool
	// Partial is true when some unfinished job has no history, so Remaining
	// leaves it out.
	Partial bool
	Jobs    []JobEstimate
}

// succeeded reports whether a job or step ran to a successful end, which is
// the only kind of sample worth timing.
func succeeded(status, conclusion string) bool {
	return status == "completed" && conclusion == "success"
}

func newStats(ds []time.Duration) Stats {
	if len(ds) == 0 {
		return Stats{}
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return Stats{P50: percentile(s, 0.5), P90: percentile(s, 0.9), N: len(s)}
}

// percentile interpolates linearly between closest ranks of sorted.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	frac := rank - float64(lo)
	return sorted[lo] + time.Duration(frac*float64(sorted[hi]-sorted[lo]))
}

// left is how much longer something typically lasting st will run, given
// it has run for elapsed. Once past the median, the p90 is the better guess.
func left(st Stats, elapsed time.Duration) time.Duration {
	if elapsed < st.P50 {
		return st.P50 - elapsed
	}
	return nonNegative(st.P90 - elapsed)
}

func nonNegative(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

// BuildHistory summarises past runs. Only successful jobs and steps count.
func BuildHistory(runs []Run) History {
	type obs struct{ start, end time.Time }
	perRun := make([]map[string]obs, 0, len(runs))
	durations := map[string][]time.Duration{}
	steps := map[string]map[string][]time.Duration{}

	for _, r := range runs {
		seen := map[string]obs{}
		for _, j := range r.Jobs {
			if !succeeded(j.Status, j.Conclusion) || j.StartedAt.IsZero() || j.CompletedAt.IsZero() {
				continue
			}
			seen[j.Name] = obs{j.StartedAt, j.CompletedAt}
			durations[j.Name] = append(durations[j.Name], nonNegative(j.CompletedAt.Sub(j.StartedAt)))
			for _, s := range j.Steps {
				if !succeeded(s.Status, s.Conclusion) || s.StartedAt.IsZero() || s.CompletedAt.IsZero() {
					continue
				}
				if steps[j.Name] == nil {
					steps[j.Name] = map[string][]time.Duration{}
				}
				steps[j.Name][s.Name] = append(steps[j.Name][s.Name], nonNegative(s.CompletedAt.Sub(s.StartedAt)))
			}
		}
		perRun = append(perRun, seen)
	}

	h := History{Runs: len(runs), Jobs: map[string]*JobHistory{}}
	for name, ds := range durations {
		jh := &JobHistory{Duration: newStats(ds), Steps: map[string]Stats{}, Preds: map[string]bool{}}
		for sn, sds := range steps[name] {
			jh.Steps[sn] = newStats(sds)
		}
		h.Jobs[name] = jh
	}

	// A job is a predecessor of another if it always finished first.
	for b := range h.Jobs {
		for p := range h.Jobs {
			if p == b {
				continue
			}
			together, always := 0, true
			for _, seen := range perRun {
				po, okP := seen[p]
				bo, okB := seen[b]
				if !okP || !okB {
					continue
				}
				together++
				if po.end.After(bo.start) {
					always = false
					break
				}
			}
			if together > 0 && always {
				h.Jobs[b].Preds[p] = true
			}
		}
	}

	gaps := map[string][]time.Duration{}
	for i, seen := range perRun {
		for b, bo := range seen {
			frontier := runs[i].RunStartedAt
			for p := range h.Jobs[b].Preds {
				if po, ok := seen[p]; ok && po.end.After(frontier) {
					frontier = po.end
				}
			}
			if frontier.IsZero() {
				continue
			}
			gaps[b] = append(gaps[b], nonNegative(bo.start.Sub(frontier)))
		}
	}
	for name, gs := range gaps {
		h.Jobs[name].Gap = newStats(gs)
	}
	return h
}

// remainingBySteps estimates what is left of a running job from its steps'
// history. ok is false when some unfinished step has never been seen before.
func remainingBySteps(j Job, jh *JobHistory, now time.Time) (time.Duration, bool) {
	var rest time.Duration
	for _, s := range j.Steps {
		if s.Status == "completed" {
			continue
		}
		st, ok := jh.Steps[s.Name]
		if !ok {
			return 0, false
		}
		if s.Status == "in_progress" {
			rest += left(st, now.Sub(s.StartedAt))
		} else {
			rest += st.P50
		}
	}
	return rest, len(j.Steps) > 0
}

// Estimate forecasts when run will finish, given the history of its
// workflow. Jobs are scheduled after their learned predecessors, so
// parallel jobs overlap rather than add up.
func Estimate(run Run, h History, now time.Time) RunEstimate {
	est := RunEstimate{Run: run, Now: now, HistoryRuns: h.Runs}
	if !run.RunStartedAt.IsZero() {
		end := now
		if run.Status == "completed" && !run.UpdatedAt.IsZero() {
			end = run.UpdatedAt
		}
		est.Elapsed = nonNegative(end.Sub(run.RunStartedAt))
	}

	present := map[string]bool{}
	for _, j := range run.Jobs {
		present[j.Name] = true
	}

	ends := map[string]time.Time{}
	jobs := make([]JobEstimate, len(run.Jobs))
	resolved := make([]bool, len(run.Jobs))

	// Running and finished jobs need nothing from the others.
	for i, j := range run.Jobs {
		jh := h.Jobs[j.Name]
		je := JobEstimate{Name: j.Name, Status: j.Status, Conclusion: j.Conclusion}
		if jh != nil {
			d := jh.Duration
			je.Typical = &d
		}
		switch j.Status {
		case "completed":
			je.State = StateDone
			if !j.StartedAt.IsZero() && !j.CompletedAt.IsZero() {
				je.Elapsed = nonNegative(j.CompletedAt.Sub(j.StartedAt))
			}
			ends[j.Name] = j.CompletedAt
			resolved[i] = true
		case "in_progress":
			je.Elapsed = nonNegative(now.Sub(j.StartedAt))
			if jh == nil {
				je.State = StateUnknown
				est.Partial = true
				ends[j.Name] = now
			} else {
				je.State = StateRunning
				if rest, ok := remainingBySteps(j, jh, now); ok {
					je.Remaining, je.Basis = rest, "steps"
				} else {
					je.Remaining, je.Basis = left(jh.Duration, je.Elapsed), "job"
				}
				if je.Elapsed > jh.Duration.P90 && je.Remaining == 0 {
					je.State = StateOverdue
				}
				ends[j.Name] = now.Add(je.Remaining)
			}
			resolved[i] = true
		}
		jobs[i] = je
	}

	// Queued and waiting jobs start once their predecessors are done. Keep
	// passing over them until nothing changes; anything left over (a cycle,
	// which history should never produce) is scheduled from now.
	blockedJob := map[string]bool{}
	for progress := true; progress; {
		progress = false
		for i, j := range run.Jobs {
			if resolved[i] {
				continue
			}
			if jobs[i].Typical == nil {
				jobs[i].State = StateUnknown
				est.Partial = true
				ends[j.Name] = now
				resolved[i] = true
				progress = true
				continue
			}
			jh := h.Jobs[j.Name]
			start, ready, blocked := run.RunStartedAt, true, j.Status == "waiting"
			for p := range jh.Preds {
				if !present[p] {
					continue
				}
				end, ok := ends[p]
				if !ok {
					ready = false
					break
				}
				if end.After(start) {
					start = end
				}
				blocked = blocked || blockedJob[p]
			}
			if !ready {
				continue
			}
			scheduleJob(&jobs[i], jh, j, start, now, blocked)
			blockedJob[j.Name] = blocked
			ends[j.Name] = now.Add(jobs[i].Remaining)
			resolved[i] = true
			progress = true
		}
	}
	for i, j := range run.Jobs {
		if !resolved[i] {
			jh := h.Jobs[j.Name]
			scheduleJob(&jobs[i], jh, j, run.RunStartedAt, now, j.Status == "waiting")
			ends[j.Name] = now.Add(jobs[i].Remaining)
		}
	}

	var last time.Time
	for i, je := range jobs {
		if je.State == StateBlocked {
			est.Blocked = true
		}
		if je.State == StateDone {
			continue
		}
		if end := ends[run.Jobs[i].Name]; end.After(last) {
			last = end
		}
	}
	if last.After(now) {
		est.Remaining = last.Sub(now)
	}
	est.ETA = now.Add(est.Remaining)
	est.Jobs = jobs
	return est
}

// scheduleJob fills in a not-yet-started job that is due to start once
// predecessorsEnd has passed. A job waiting on approval (or behind one) is
// assumed to be approved now, with no further gap.
func scheduleJob(je *JobEstimate, jh *JobHistory, j Job, predecessorsEnd, now time.Time, blocked bool) {
	je.Basis = "job"
	if blocked {
		je.State = StateBlocked
	} else {
		je.State = StatePending
	}
	start := now
	if predecessorsEnd.After(start) {
		start = predecessorsEnd
	}
	if !blocked && j.Status != "waiting" {
		gapEnd := predecessorsEnd.Add(jh.Gap.P50)
		if predecessorsEnd.IsZero() {
			gapEnd = now
		}
		if gapEnd.After(start) {
			start = gapEnd
		}
	}
	je.Remaining = start.Add(jh.Duration.P50).Sub(now)
}
