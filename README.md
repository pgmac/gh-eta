# gh-eta

A [`gh`](https://cli.github.com/) extension that tells you how long a running GitHub Actions workflow run has left, based on how long the same workflow took before.

A release run waiting for its signing job to be approved:

```text
$ gh eta -R pgmac-net/nagios-android
pgmac-net/nagios-android · Release · run 39003305046 · v0.4.0 (push)
Release 0.4.0 (#124)
https://github.com/pgmac-net/nagios-android/actions/runs/39003305046
elapsed 10m50s · BLOCKED: waiting for approval · ~19s once approved
history: 5 successful runs

JOB                         STATUS   ELAPSED  P50    P90     REMAINING
build (unsigned)            success  7m36s    9m57s  10m26s  -
sign and draft the release  waiting  -        19s    52s     blocked (+19s)
```

## Install

```sh
gh extension install pgmac/gh-eta
```

Prebuilt binaries are published for Linux, macOS and Windows on every release. To build from source instead: `go install github.com/pgmac/gh-eta@latest` (Go 1.25+), then `gh extension install .` from a clone.

## Usage

```sh
gh eta                      # every active run in the current repo
gh eta 39003305046          # one run (also works on finished runs)
gh eta -R owner/repo        # another repo
gh eta --owner pgmac,pgmac-net   # every active run across all repos of these users/orgs
gh eta -w                   # refresh every 15s until the runs finish
gh eta --json               # machine-readable output
```

| Flag | Default | Meaning |
|---|---|---|
| `-R, --repo OWNER/REPO` | current repo | Repository to look in |
| `--owner LOGIN[,LOGIN]` | | Scan all non-archived repos of these users or orgs (your private repos included when `LOGIN` is you) |
| `-n, --history N` | 10 | Number of past successful runs to learn from (1–100) |
| `--same-branch` | off | Only learn from runs on the same branch (tag runs then only match the same tag) |
| `--any-event` | off | Learn from runs of every trigger event. By default only runs with the same event (`push`, `pull_request`, …) count, because different events often run different jobs |
| `-w, --watch` | off | Redraw until the runs finish |
| `-i, --interval` | 15s | Redraw interval for `--watch` (minimum 5s) |
| `--json` | off | Print a JSON array of estimates |

### Reading the output

- **ELAPSED** is how long the job has run (or ran). **P50 / P90** are the median and 90th-percentile durations of that job across the history.
- **REMAINING** is the forecast for that job:
  - `4m10s` — from the job's own history; `(steps)` means it was worked out step by step for the step in progress.
  - `blocked (+19s)` — the job is waiting for an environment approval; it needs that long once approved.
  - `overdue` — the job has run past its P90 and nothing in its history says how much longer it will take.
  - `?` — the job has never succeeded before, so it is left out of the total (`some jobs have no history`).
- The headline gives the run's remaining time and clock ETA. When a job is waiting for approval it shows how long the run needs once approved instead.

## How it works

Whole-run duration is a poor baseline: it includes queueing and approval waits. gh-eta works per job and per step instead, and learns the order jobs run in from history so jobs running in parallel overlap rather than add up. See [docs/how-it-works.md](docs/how-it-works.md) for the model and measured accuracy.

## Development

```sh
go test -race ./...
go vet ./...
golangci-lint run          # v2
```

`replay_live_test.go` (build tag `live`) replays real history against the estimator and prints the ETA error at 10–90 % through each run:

```sh
GH_ETA_REPO=owner/repo GH_ETA_WORKFLOW=<workflow id> GH_ETA_EVENT=push \
  go test -tags live -run LiveReplay -v
```

Releases: push a `v*` tag; [cli/gh-extension-precompile](https://github.com/cli/gh-extension-precompile) builds the binaries with build-provenance attestations.

## Licence

MIT
