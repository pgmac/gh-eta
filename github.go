package main

import (
	"fmt"
	"net/url"
	"strconv"
	"sync"
)

// API is the slice of the GitHub REST client that gh-eta uses.
type API interface {
	Get(path string, resp interface{}) error
}

const fetchWorkers = 8

type jobsPage struct {
	TotalCount int   `json:"total_count"`
	Jobs       []Job `json:"jobs"`
}

type runsPage struct {
	TotalCount   int   `json:"total_count"`
	WorkflowRuns []Run `json:"workflow_runs"`
}

type repoInfo struct {
	FullName string `json:"full_name"`
	Archived bool   `json:"archived"`
	Disabled bool   `json:"disabled"`
}

// fetchJobs returns every job of the latest attempt of a run.
func fetchJobs(c API, repo string, runID int64) ([]Job, error) {
	var jobs []Job
	for page := 1; ; page++ {
		var p jobsPage
		path := fmt.Sprintf("repos/%s/actions/runs/%d/jobs?per_page=100&page=%d", repo, runID, page)
		if err := c.Get(path, &p); err != nil {
			return nil, err
		}
		jobs = append(jobs, p.Jobs...)
		if len(p.Jobs) == 0 || len(jobs) >= p.TotalCount {
			return jobs, nil
		}
	}
}

// fetchRun returns one run with its jobs.
func fetchRun(c API, repo string, runID int64) (Run, error) {
	var r Run
	if err := c.Get(fmt.Sprintf("repos/%s/actions/runs/%d", repo, runID), &r); err != nil {
		return Run{}, err
	}
	r.Repo = repo
	jobs, err := fetchJobs(c, repo, runID)
	if err != nil {
		return Run{}, err
	}
	r.Jobs = jobs
	return r, nil
}

// activeRuns returns the runs of repo that have not completed, with jobs.
// Active runs are always among the most recent, so one page is enough.
func activeRuns(c API, repo string) ([]Run, error) {
	var p runsPage
	if err := c.Get(fmt.Sprintf("repos/%s/actions/runs?per_page=50", repo), &p); err != nil {
		return nil, err
	}
	var runs []Run
	for _, r := range p.WorkflowRuns {
		if r.Status == "completed" {
			continue
		}
		r.Repo = repo
		runs = append(runs, r)
	}
	if err := withJobs(c, runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// withJobs fills in the jobs of each run, a few runs at a time.
func withJobs(c API, runs []Run) error {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, fetchWorkers)
	)
	for i := range runs {
		wg.Add(1)
		go func(r *Run) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			jobs, err := fetchJobs(c, r.Repo, r.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			r.Jobs = jobs
		}(&runs[i])
	}
	wg.Wait()
	return firstErr
}

// HistoryQuery picks which past runs a run is compared with.
type HistoryQuery struct {
	Repo       string
	WorkflowID int64
	Event      string // empty for any event
	Branch     string // empty for any branch
	Limit      int
	Exclude    int64
}

func (q HistoryQuery) key() string {
	return fmt.Sprintf("%s|%d|%s|%s|%d", q.Repo, q.WorkflowID, q.Event, q.Branch, q.Limit)
}

// fetchHistory returns up to q.Limit successful past runs, with jobs.
func fetchHistory(c API, q HistoryQuery) ([]Run, error) {
	v := url.Values{}
	v.Set("status", "success")
	v.Set("exclude_pull_requests", "true")
	v.Set("per_page", strconv.Itoa(min(q.Limit+1, 100)))
	if q.Event != "" {
		v.Set("event", q.Event)
	}
	if q.Branch != "" {
		v.Set("branch", q.Branch)
	}
	var p runsPage
	if err := c.Get(fmt.Sprintf("repos/%s/actions/workflows/%d/runs?%s", q.Repo, q.WorkflowID, v.Encode()), &p); err != nil {
		return nil, err
	}
	runs := make([]Run, 0, q.Limit)
	for _, r := range p.WorkflowRuns {
		if r.ID == q.Exclude || len(runs) == q.Limit {
			continue
		}
		r.Repo = q.Repo
		runs = append(runs, r)
	}
	if err := withJobs(c, runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// ownerRepos lists the live (not archived or disabled) repos of a user or
// organisation. For the signed-in user it includes private repos.
func ownerRepos(c API, owner string) ([]string, error) {
	var who struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if err := c.Get("users/"+url.PathEscape(owner), &who); err != nil {
		return nil, err
	}
	base := fmt.Sprintf("users/%s/repos?type=owner", url.PathEscape(owner))
	if who.Type == "Organization" {
		base = fmt.Sprintf("orgs/%s/repos?type=all", url.PathEscape(owner))
	} else {
		var me struct {
			Login string `json:"login"`
		}
		if err := c.Get("user", &me); err == nil && me.Login == who.Login {
			base = "user/repos?affiliation=owner"
		}
	}
	var names []string
	for page := 1; ; page++ {
		var repos []repoInfo
		if err := c.Get(fmt.Sprintf("%s&per_page=100&page=%d", base, page), &repos); err != nil {
			return nil, err
		}
		for _, r := range repos {
			if !r.Archived && !r.Disabled {
				names = append(names, r.FullName)
			}
		}
		if len(repos) < 100 {
			return names, nil
		}
	}
}

// activeRunsIn scans many repos for active runs. Repos whose runs cannot be
// read (Actions disabled, no access) are skipped.
func activeRunsIn(c API, repos []string) []Run {
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all []Run
		sem = make(chan struct{}, fetchWorkers*2)
	)
	for _, repo := range repos {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			runs, err := activeRuns(c, repo)
			if err != nil {
				return
			}
			mu.Lock()
			all = append(all, runs...)
			mu.Unlock()
		}(repo)
	}
	wg.Wait()
	return all
}
