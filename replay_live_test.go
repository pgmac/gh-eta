//go:build live

package main

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// TestLiveReplay measures estimate accuracy on real history: each recent
// successful run is rewound to points through its life and estimated from
// the runs before it.
//
//	GH_ETA_REPO=owner/repo GH_ETA_WORKFLOW=<id> [GH_ETA_EVENT=push] \
//	  go test -tags live -run LiveReplay -v
func TestLiveReplay(t *testing.T) {
	repo := os.Getenv("GH_ETA_REPO")
	wf, _ := strconv.ParseInt(os.Getenv("GH_ETA_WORKFLOW"), 10, 64)
	if repo == "" || wf == 0 {
		t.Skip("set GH_ETA_REPO and GH_ETA_WORKFLOW")
	}
	c, err := api.DefaultRESTClient()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := fetchHistory(c, HistoryQuery{Repo: repo, WorkflowID: wf, Event: os.Getenv("GH_ETA_EVENT"), Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+3 <= len(runs); i++ {
		target, past := runs[i], runs[i+1:min(i+11, len(runs))]
		h := BuildHistory(past)
		first, last := target.RunStartedAt, time.Time{}
		for _, j := range target.Jobs {
			if j.CompletedAt.After(last) {
				last = j.CompletedAt
			}
		}
		line := ""
		for _, f := range []float64{0.1, 0.25, 0.5, 0.75, 0.9} {
			now := first.Add(time.Duration(f * float64(last.Sub(first))))
			e := Estimate(snapshotAt(target, now), h, now)
			line += " " + strconv.Itoa(int(f*100)) + "%:" + fmtSigned(e.ETA.Sub(last))
		}
		t.Logf("run %d (%s, %d past):%s", target.ID, fmtDur(last.Sub(first)), len(past), line)
	}
}

func fmtSigned(d time.Duration) string {
	if d < 0 {
		return "-" + fmtDur(-d)
	}
	return "+" + fmtDur(d)
}
