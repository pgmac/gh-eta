package main

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func mins(m float64) time.Duration { return time.Duration(m * float64(time.Minute)) }

// job builds a successful job that ran from start to end minutes after t0.
func job(name string, start, end float64, steps ...Step) Job {
	return Job{Name: name, Status: "completed", Conclusion: "success",
		StartedAt: at(mins(start)), CompletedAt: at(mins(end)), Steps: steps}
}

func step(name string, start, end float64) Step {
	return Step{Name: name, Status: "completed", Conclusion: "success",
		StartedAt: at(mins(start)), CompletedAt: at(mins(end))}
}

func pastRun(id int64, jobs ...Job) Run {
	return Run{ID: id, Status: "completed", Conclusion: "success", RunStartedAt: t0, Jobs: jobs}
}

// snapshotAt rewinds a finished run to how the API showed it at time t.
func snapshotAt(r Run, t time.Time) Run {
	r.Status, r.Conclusion = "in_progress", ""
	jobs := make([]Job, len(r.Jobs))
	for i, j := range r.Jobs {
		switch {
		case !j.CompletedAt.After(t):
		case !j.StartedAt.After(t):
			j.Status, j.Conclusion, j.CompletedAt = "in_progress", "", time.Time{}
		default:
			j.Status, j.Conclusion, j.StartedAt, j.CompletedAt = "queued", "", time.Time{}, time.Time{}
		}
		steps := make([]Step, len(j.Steps))
		for k, s := range j.Steps {
			switch {
			case !s.CompletedAt.After(t):
			case !s.StartedAt.After(t):
				s.Status, s.Conclusion, s.CompletedAt = "in_progress", "", time.Time{}
			default:
				s.Status, s.Conclusion, s.StartedAt, s.CompletedAt = "queued", "", time.Time{}, time.Time{}
			}
			steps[k] = s
		}
		j.Steps = steps
		jobs[i] = j
	}
	r.Jobs = jobs
	return r
}

func approx(t *testing.T, what string, got, want time.Duration) {
	t.Helper()
	if diff := got - want; diff > time.Second || diff < -time.Second {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func TestPercentile(t *testing.T) {
	s := newStats([]time.Duration{4 * time.Minute, time.Minute, 3 * time.Minute, 2 * time.Minute})
	approx(t, "p50", s.P50, mins(2.5))
	approx(t, "p90", s.P90, mins(3.7))
	if s.N != 4 {
		t.Errorf("N = %d, want 4", s.N)
	}
	one := newStats([]time.Duration{time.Minute})
	if one.P50 != time.Minute || one.P90 != time.Minute {
		t.Errorf("single sample stats = %+v", one)
	}
	if (newStats(nil) != Stats{}) {
		t.Error("empty stats not zero")
	}
}

func TestBuildHistoryLearnsOrdering(t *testing.T) {
	runs := []Run{
		pastRun(1, job("lint", 0, 2), job("test", 0, 5), job("deploy", 5.5, 6.5)),
		pastRun(2, job("lint", 0, 3), job("test", 0.5, 4), job("deploy", 4.5, 5.5)),
	}
	h := BuildHistory(runs)
	deploy := h.Jobs["deploy"]
	if !deploy.Preds["lint"] || !deploy.Preds["test"] {
		t.Errorf("deploy preds = %v, want lint and test", deploy.Preds)
	}
	// lint and test overlap, so neither waits for the other.
	if len(h.Jobs["lint"].Preds) != 0 || len(h.Jobs["test"].Preds) != 0 {
		t.Errorf("parallel jobs got preds: lint=%v test=%v", h.Jobs["lint"].Preds, h.Jobs["test"].Preds)
	}
	approx(t, "deploy gap", deploy.Gap.P50, mins(0.5))
	approx(t, "deploy duration", deploy.Duration.P50, time.Minute)
}

func TestBuildHistoryIgnoresFailedAndSkipped(t *testing.T) {
	skipped := Job{Name: "sign", Status: "completed", Conclusion: "skipped", StartedAt: t0, CompletedAt: t0}
	h := BuildHistory([]Run{pastRun(1, job("build", 0, 10), skipped)})
	if _, ok := h.Jobs["sign"]; ok {
		t.Error("skipped job counted in history")
	}
}

func TestEstimateSequential(t *testing.T) {
	past := []Run{
		pastRun(1, job("build", 0, 10), job("sign", 11, 12)),
		pastRun(2, job("build", 0, 10), job("sign", 11, 12)),
	}
	h := BuildHistory(past)
	cur := snapshotAt(pastRun(3, job("build", 0, 10), job("sign", 11, 12)), at(mins(4)))
	e := Estimate(cur, h, at(mins(4)))
	// 6m of build, 1m gap, 1m sign.
	approx(t, "remaining", e.Remaining, mins(8))
	if e.Blocked || e.Partial {
		t.Errorf("blocked=%v partial=%v", e.Blocked, e.Partial)
	}
	if e.Jobs[0].State != StateRunning || e.Jobs[1].State != StatePending {
		t.Errorf("states = %s, %s", e.Jobs[0].State, e.Jobs[1].State)
	}
}

func TestEstimateParallelJobsOverlap(t *testing.T) {
	mk := func(id int64) Run {
		return pastRun(id, job("lint", 0, 2), job("test", 0, 5), job("deploy", 5, 6))
	}
	h := BuildHistory([]Run{mk(1), mk(2)})
	e := Estimate(snapshotAt(mk(3), at(time.Minute)), h, at(time.Minute))
	// test has 4m left, then deploy 1m. Summing lint would give 6m.
	approx(t, "remaining", e.Remaining, mins(5))
}

func TestEstimateBlockedOnApproval(t *testing.T) {
	past := []Run{
		pastRun(1, job("build", 0, 10), job("sign", 30, 31)),
		pastRun(2, job("build", 0, 10), job("sign", 15, 16)),
	}
	h := BuildHistory(past)
	cur := pastRun(3, job("build", 0, 8))
	cur.Status = "waiting"
	cur.Jobs = append(cur.Jobs, Job{Name: "sign", Status: "waiting", StartedAt: at(mins(8))})
	e := Estimate(cur, h, at(mins(12)))
	if !e.Blocked {
		t.Fatal("not blocked")
	}
	if e.Jobs[1].State != StateBlocked {
		t.Errorf("sign state = %s", e.Jobs[1].State)
	}
	// The usual approval wait is not counted once a job is actually waiting.
	approx(t, "remaining", e.Remaining, time.Minute)
}

func TestEstimateUsesSteps(t *testing.T) {
	mk := func(id int64) Run {
		return pastRun(id, job("build", 0, 10,
			step("checkout", 0, 1), step("compile", 1, 9), step("upload", 9, 10)))
	}
	h := BuildHistory([]Run{mk(1), mk(2)})
	// A slow checkout this time: 3m in and still checking out.
	cur := pastRun(3, Job{Name: "build", Status: "in_progress", StartedAt: t0, Steps: []Step{
		{Name: "checkout", Status: "in_progress", StartedAt: t0},
		{Name: "compile", Status: "queued"},
		{Name: "upload", Status: "queued"},
	}})
	e := Estimate(cur, h, at(mins(3)))
	if e.Jobs[0].Basis != "steps" {
		t.Errorf("basis = %q, want steps", e.Jobs[0].Basis)
	}
	// Job-level would say 7m; the steps still to run take 9m.
	approx(t, "remaining", e.Remaining, mins(9))
}

func TestEstimateFallsBackToJobWhenStepUnknown(t *testing.T) {
	mk := func(id int64) Run { return pastRun(id, job("build", 0, 10, step("compile", 0, 10))) }
	h := BuildHistory([]Run{mk(1)})
	cur := pastRun(2, Job{Name: "build", Status: "in_progress", StartedAt: t0, Steps: []Step{
		{Name: "brand new step", Status: "in_progress", StartedAt: t0},
	}})
	e := Estimate(cur, h, at(mins(4)))
	if e.Jobs[0].Basis != "job" {
		t.Errorf("basis = %q, want job", e.Jobs[0].Basis)
	}
	approx(t, "remaining", e.Remaining, mins(6))
}

func TestEstimateUnknownJob(t *testing.T) {
	h := BuildHistory([]Run{pastRun(1, job("build", 0, 10))})
	cur := pastRun(2, Job{Name: "build", Status: "in_progress", StartedAt: t0},
		Job{Name: "new-job", Status: "queued"})
	e := Estimate(cur, h, at(mins(2)))
	if !e.Partial {
		t.Error("partial not set")
	}
	if e.Jobs[1].State != StateUnknown {
		t.Errorf("new-job state = %s", e.Jobs[1].State)
	}
	approx(t, "remaining", e.Remaining, mins(8))
}

func TestEstimateOverdue(t *testing.T) {
	h := BuildHistory([]Run{pastRun(1, job("build", 0, 10)), pastRun(2, job("build", 0, 12))})
	cur := pastRun(3, Job{Name: "build", Status: "in_progress", StartedAt: t0})
	e := Estimate(cur, h, at(mins(20)))
	if e.Jobs[0].State != StateOverdue {
		t.Errorf("state = %s, want overdue", e.Jobs[0].State)
	}
	if e.Remaining != 0 {
		t.Errorf("remaining = %s, want 0", e.Remaining)
	}
}

func TestEstimateReplayIsExactOnRepeatRun(t *testing.T) {
	mk := func(id int64) Run {
		return pastRun(id,
			job("build", 0.2, 9, step("setup", 0.2, 1), step("gradle", 1, 9)),
			job("sign", 9.3, 10, step("sign", 9.3, 10)))
	}
	h := BuildHistory([]Run{mk(1), mk(2), mk(3)})
	finish := at(mins(10))
	for _, m := range []float64{0.5, 3, 8.9, 9.1, 9.5} {
		now := at(mins(m))
		e := Estimate(snapshotAt(mk(4), now), h, now)
		approx(t, "ETA error at "+fmtDur(mins(m)), e.ETA.Sub(finish), 0)
	}
}
