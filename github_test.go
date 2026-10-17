package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeAPI answers GETs from canned JSON, keyed by path without its query
// string, and records every path asked for.
type fakeAPI struct {
	mu     sync.Mutex
	routes map[string]func(q url.Values) string
	calls  []string
}

func (f *fakeAPI) Get(path string, resp interface{}) error {
	f.mu.Lock()
	f.calls = append(f.calls, path)
	f.mu.Unlock()
	p, rawQuery, _ := strings.Cut(path, "?")
	h, ok := f.routes[p]
	if !ok {
		return fmt.Errorf("HTTP 404: %s", p)
	}
	q, _ := url.ParseQuery(rawQuery)
	return json.Unmarshal([]byte(h(q)), resp)
}

func static(body string) func(url.Values) string { return func(url.Values) string { return body } }

func TestFetchJobsPaginates(t *testing.T) {
	f := &fakeAPI{routes: map[string]func(url.Values) string{
		"repos/o/r/actions/runs/7/jobs": func(q url.Values) string {
			if q.Get("page") == "1" {
				return `{"total_count":2,"jobs":[{"name":"a"}]}`
			}
			return `{"total_count":2,"jobs":[{"name":"b"}]}`
		},
	}}
	jobs, err := fetchJobs(f, "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[1].Name != "b" {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestActiveRunsSkipsCompleted(t *testing.T) {
	f := &fakeAPI{routes: map[string]func(url.Values) string{
		"repos/o/r/actions/runs": static(`{"workflow_runs":[
			{"id":1,"status":"completed"},
			{"id":2,"status":"in_progress","run_started_at":"2026-10-17T04:00:00Z"},
			{"id":3,"status":"waiting"}]}`),
		"repos/o/r/actions/runs/2/jobs": static(`{"total_count":1,"jobs":[{"name":"build","status":"in_progress"}]}`),
		"repos/o/r/actions/runs/3/jobs": static(`{"total_count":0,"jobs":[]}`),
	}}
	runs, err := activeRuns(f, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != 2 || runs[1].ID != 3 {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[0].Repo != "o/r" || len(runs[0].Jobs) != 1 || runs[0].RunStartedAt.IsZero() {
		t.Errorf("run 2 = %+v", runs[0])
	}
}

func TestFetchHistoryFiltersAndExcludes(t *testing.T) {
	var got url.Values
	f := &fakeAPI{routes: map[string]func(url.Values) string{
		"repos/o/r/actions/workflows/9/runs": func(q url.Values) string {
			got = q
			return `{"workflow_runs":[{"id":5},{"id":4},{"id":3},{"id":2}]}`
		},
		"repos/o/r/actions/runs/4/jobs": static(`{"total_count":0,"jobs":[]}`),
		"repos/o/r/actions/runs/3/jobs": static(`{"total_count":0,"jobs":[]}`),
	}}
	runs, err := fetchHistory(f, HistoryQuery{Repo: "o/r", WorkflowID: 9, Event: "push", Limit: 2, Exclude: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != 4 || runs[1].ID != 3 {
		t.Errorf("runs = %+v", runs)
	}
	if got.Get("status") != "success" || got.Get("event") != "push" || got.Get("branch") != "" || got.Get("per_page") != "3" {
		t.Errorf("query = %v", got)
	}
}

func TestOwnerReposUsesRightEndpoint(t *testing.T) {
	repos := static(`[{"full_name":"x/live"},{"full_name":"x/old","archived":true}]`)
	cases := []struct {
		name, user, route string
	}{
		{"org", `{"login":"x","type":"Organization"}`, "orgs/x/repos"},
		{"me", `{"login":"x","type":"User"}`, "user/repos"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{routes: map[string]func(url.Values) string{
				"users/x": static(tc.user),
				"user":    static(`{"login":"x"}`),
				tc.route:  repos,
			}}
			names, err := ownerRepos(f, "x")
			if err != nil {
				t.Fatal(err)
			}
			if len(names) != 1 || names[0] != "x/live" {
				t.Errorf("names = %v", names)
			}
		})
	}
}

func TestActiveRunsInSkipsUnreadableRepos(t *testing.T) {
	f := &fakeAPI{routes: map[string]func(url.Values) string{
		"repos/o/a/actions/runs":        static(`{"workflow_runs":[{"id":1,"status":"queued"}]}`),
		"repos/o/a/actions/runs/1/jobs": static(`{"total_count":0,"jobs":[]}`),
	}}
	runs := activeRunsIn(f, []string{"o/a", "o/no-actions"})
	if len(runs) != 1 || runs[0].Repo != "o/a" {
		t.Errorf("runs = %+v", runs)
	}
}
