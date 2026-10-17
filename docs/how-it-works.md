# How gh-eta estimates

## Inputs

For each active run gh-eta fetches:

- the run and all its jobs, with steps (`/actions/runs/{id}` and `/actions/runs/{id}/jobs`);
- the last *N* (default 10) **successful** runs of the same workflow, with the same trigger event by default, and their jobs.

Failed, cancelled and skipped jobs and steps are never timed. History is fetched once per workflow per invocation and kept for the whole of a `--watch` session.

## History model

From the past runs, per job name:

| What | How |
|---|---|
| Duration P50 / P90 | `completed_at − started_at`, linear-interpolated percentiles |
| Step durations | The same, per step name |
| Predecessors | Job *P* is a predecessor of job *B* if, in every past run where both ran, *P* finished before *B* started. This is a stand-in for the workflow's `needs:` graph that needs no YAML parsing and copes with matrix and reusable-workflow job names |
| Start gap P50 | Delay from the last predecessor finishing (or the run starting) to the job starting: runner pickup, and for gated jobs the usual approval wait |

GitHub resets a gated job's `started_at` when the approval is given, so job durations do not include approval waits; only start gaps do.

## Forecast

Each job of the current run is scheduled in turn:

1. **Completed** — ends when it ended.
2. **In progress** — remaining time from its steps if every unfinished step has history: the P50 of each step still queued, plus what is left of the step in progress. Otherwise from the job's own P50. For anything already past its P50, the P90 is used instead ("it's slow this time, assume slow"). Past the P90 the job is `overdue` and counted as ending now.
3. **Not started** — starts once its predecessors in this run are forecast to end, plus its usual start gap, and then takes its P50.
4. **Waiting for approval** — `blocked`. It, and anything after it, is scheduled as if approved now, with no gap. The headline then gives the time needed once approved rather than a clock ETA.
5. **No history** — `unknown`, left out; the estimate is flagged as partial.

The run's remaining time is the latest forecast job end minus now. Because jobs are placed after their predecessors, rather than summed, parallel jobs overlap.

## Measured accuracy

`replay_live_test.go` rewinds each of the last 20 successful runs to 10, 25, 50, 75 and 90 % of its length and estimates it from up to 10 runs before it. The error is forecast finish minus actual finish (negative = finished later than forecast). Measured 2026-10-17:

| Workflow | Typical run | Typical error | Notes |
|---|---|---|---|
| pgmac-net/gh-issues-tui CI (push) | 15 s – 5 m | ±20 s | One run with a different job set was 1–2 m pessimistic |
| pgmac-net/nagios-android CI (pull_request) | 6 – 8 m | −30 to −60 s early on, −7 to −15 s at 90 % | Builds were trending slower than their 10-run median |
| pgmac-net/nagios-android Release (push) | 9 – 12 m | ±1–2 m, worse with only 2–3 runs of history | The 19 m run included a ~7 m approval wait |

Switching to the P90 once a step or job passes its P50 cut the late-run error on nagios-android CI from about −50 s to about −10 s.

## Known limits

- Runs where paths filters or conditions skip most jobs look like a full run until those jobs are skipped, so the estimate is pessimistic early.
- A plain median lags a trend: if a workflow is getting slower, estimates run low until the history catches up. Shorten it with `-n`.
- Jobs created during the run (dynamic matrices) are invisible until they appear.
- Re-run attempts are estimated from the latest attempt's jobs only.
