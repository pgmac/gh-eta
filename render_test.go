package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFmtDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                     "0s",
		45 * time.Second:                      "45s",
		7*time.Minute + 36*time.Second:        "7m36s",
		time.Hour + 2*time.Minute + 9e9:       "1h02m",
		59*time.Second + 600*time.Millisecond: "1m00s",
	} {
		if got := fmtDur(d); got != want {
			t.Errorf("fmtDur(%v) = %q, want %q", d, got, want)
		}
	}
}

func blockedEstimate() RunEstimate {
	h := BuildHistory([]Run{pastRun(1, job("build", 0, 10), job("sign", 11, 12))})
	cur := Run{ID: 42, Repo: "o/r", WorkflowName: "Release", Branch: "v1.0.0", Event: "push",
		Status: "waiting", RunStartedAt: t0, Jobs: []Job{
			job("build", 0, 8),
			{Name: "sign", Status: "waiting", StartedAt: at(mins(8))},
		}}
	return Estimate(cur, h, at(mins(9)))
}

func TestRenderTextBlocked(t *testing.T) {
	var b bytes.Buffer
	if err := renderText(&b, []RunEstimate{blockedEstimate()}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"o/r · Release · run 42 · v1.0.0 (push)",
		"elapsed 9m00s · BLOCKED: waiting for approval · ~1m00s once approved",
		"history: 1 successful runs",
		"blocked (+1m00s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderJSON(t *testing.T) {
	var b bytes.Buffer
	if err := renderJSON(&b, []RunEstimate{blockedEstimate()}); err != nil {
		t.Fatal(err)
	}
	var got []jsonRun
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Blocked || got[0].Remaining != 60 || len(got[0].Jobs) != 2 {
		t.Fatalf("got %+v", got)
	}
	if j := got[0].Jobs[0]; j.State != StateDone || j.Elapsed != 480 || j.P50 == nil || *j.P50 != 600 {
		t.Errorf("build job = %+v", j)
	}
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"123", "-R", "o/r", "-n", "5", "--same-branch"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.runID != 123 || o.repo != "o/r" || o.history != 5 || !o.sameBranch || o.interval != 15*time.Second {
		t.Errorf("options = %+v", o)
	}
	for _, bad := range [][]string{
		{"abc"},
		{"1", "2"},
		{"1", "--owner", "x"},
		{"-R", "o/r", "--owner", "x"},
		{"-n", "0"},
		{"--watch", "-i", "1s"},
	} {
		if _, err := parseArgs(bad, io.Discard); err == nil {
			t.Errorf("parseArgs(%q) succeeded, want error", bad)
		}
	}
}
