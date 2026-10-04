# trace MCP: filtering and analysing browser-use run traces

An MCP server (plus a CLI over the same engine) that answers questions about the scraper's runs and
the browser-use agent traces they produce.

It exists because the alternative is what happened first: fetching 600 traces by hand, one HTTP call
each, and pushing every one of them through an agent's context to find out where the time went. The
engine fetches once into a local cache and computes the aggregates itself, so a broad question costs
one tool call and a narrow follow-up costs none.

## What it answers

Real examples, each of which is now a single call:

| Question | Tool |
| --- | --- |
| "Where does the sweep spend its time?" | `run_stats` + `sweep_outcomes` |
| "How many companies got skipped, failed, or actually fetched?" | `sweep_outcomes` |
| "Which companies timed out, and how many steps did they manage?" | `list_runs` + `step_stats` |
| "Is the agent thrashing, or is each step just slow?" | `step_stats` (actions + per-step latency) |
| "Find every run where the agent got stuck in a shadow DOM" | `search_steps` |
| "What did this one run actually do?" | `trace_digest` |
| "Did the fetch after the agent find anything?" | `get_run_log` |

## What the first pass found

Run against the live corpus, which is the point of it existing:

- **601 `career_listings` runs, 45.3 hours** of wall time; median 30s, p90 456s, max 73539s (an
  orphaned run). A recent sweep's own log covers 154 companies: **102 had no remote roles**, 22
  reached a fetch, 16 failed, 8 returned no listings URL, 5 were blocked by robots.
- The agent is **not thrashing**: median **6 steps**, p90 11, max 19, and 128 of 128 runs that reached
  a final answer succeeded. The cost is per-step latency, not step count.
- Actions per run are a short purposeful sequence — across 160 traces: 557 clicks, 219 inputs, 149
  scrolls, 135 waits — i.e. roughly 3–4 clicks and one or two inputs each.
- **16 runs per sweep are killed by "agent timed out"**, each burning the full 12-minute budget and
  none reaching a final answer. Conversely `abbott-laboratories` fails the same way every sweep: its
  `Search jobs` button lives in a shadow DOM and the click times out after 15s.

Two things follow, and both are about *which* companies cost an agent run:

1. The listings-URL cache covers the two large buckets — `no_remote_roles` (102) and `fetched` (22) —
   so those stop costing an agent run on a warm sweep. That is 81% of companies.
2. `agent_failed`, `listings_none` and `robots_disallowed` are **not** cached (a failure is not an
   answer), so they run the agent again on every sweep. At ~4.7 minutes per agent run, the 16
   timeouts alone are ~3 hours per sweep, every sweep. That is the next lever.

## Sources

| Source | Reads | Use |
| --- | --- | --- |
| `local` (default) | `jobs.db` + `data/traces` + `data/jobs` | Offline, fast, as fresh as your checkout |
| `remote` | the jobs-app HTTP API | The live NAS corpus |

A remote trace is downloaded into the cache on first use; a local one is read where the pipeline
already wrote it. Either way every analysis tool reads the cache, so syncing once makes later
questions instant.

## Tools

| Tool | What it does |
| --- | --- |
| `list_runs` | Filter runs by platform, status, company, duration, error text, time window, id range, trace presence; sort and page |
| `run_stats` | Count, duration percentiles, total wall hours, status/platform breakdown, top errors, trace coverage |
| `sync_traces` | Fetch traces and logs for matching runs into the cache (idempotent) |
| `trace_digest` | One run condensed: steps, distinct URLs, action histogram, first/last URL, terminal state, per-step list |
| `search_steps` | Regex across cached step fields (`url`, `next_goal`, `thinking`, `evaluation_previous_goal`, `memory`, `actions`) |
| `step_stats` | Steps per run, step-count buckets, action histogram, final-answer rate, per-step latency and the slowest steps |
| `sweep_outcomes` | Per-company outcomes for a whole sweep, parsed from the batch log: skipped / failed / blocked / fetched, with the totals |
| `get_run_log` | The run's log lines, optionally grepped, falling back to the parent sweep's lines |

### Where a run's log actually is

A per-company run during a sweep has an **empty log of its own**. `batch` runs each company's `scrape`
as a child process, which inherits the batch's stdout, so the whole sweep's output lands in the
`career_batch` run's file. Asking the API for the child's log and getting nothing is the normal case,
not a missing file — so `get_run_log` falls back to the lines its parent captured (they are prefixed
`run_id=<id>`, which makes the attribution exact) and reports which it used as `origin`. `sweep_outcomes`
reads the batch log directly.

Every filter tool takes the same vocabulary, and an unknown filter is an error rather than a silent
no-op — a typo must not quietly return everything.

## Registering it

### With dsh

Add to your profile's patch layer (`~/.dsh/profiles/<profile>/cordis.patch.yml`):

```yaml
- id: mcp-traces
  name: "@deepseek-ai/dsh-mcp-client"
  config:
    serverName: traces
    transport: stdio
    command: python3
    args:
      - /Users/cris/Projects/jobs/jobs-app/tools/tracemcp/server.py
    env:
      TRACE_SOURCE: remote
      JOBS_API: https://jobapp.siggy-lab.org
      TRACE_CACHE: /Users/cris/Projects/jobs/jobs-app/.tracecache
```

Tools then appear as `mcp__traces__run_stats`, `mcp__traces__search_steps`, and so on. Check the
composed config before restarting:

```bash
dsh --dump-config --profile web | grep -A6 mcp-traces
```

### With any other MCP client

It is a standard stdio server. Point the client at `python3 .../tools/tracemcp/server.py`, or set
`TRACE_SOURCE=remote` in its `env` block to read the live corpus.

No dependencies, and deliberately so: it runs with whatever `python3` is on `PATH`. The repo's
browser venv was pinned to a Homebrew Python that a routine `brew upgrade` deleted, taking its
installed packages with it; a tool whose only job is to answer questions should not be the next thing
that breaks for that reason.

## CLI

The CLI dispatches through the same functions the MCP server exposes, so it is a faithful harness for
the tools rather than a second implementation that can drift:

```bash
cd tools/tracemcp
python3 cli.py stats  --platform career_listings          # the shape of the corpus
python3 cli.py sync   --status error --max-runs 25        # fetch what you want to study
python3 cli.py steps  --platform career_listings          # step/action/latency stats
python3 cli.py digest 615                                 # one run, condensed
python3 cli.py search 'shadow DOM' --platform career_listings
python3 cli.py log    615 --grep resolution
```

Add `--source remote --cache /tmp/traces` to work against the live API without touching the repo's
cache.

## The trace format

One JSON object per line, written by `worker/agent_trace.py`:

```json
{"event": "step", "step": 3, "url": "https://jobs.acme.test/careers?q=x",
 "thinking": null, "evaluation_previous_goal": "...", "memory": "...", "next_goal": "...",
 "actions": [{"scroll": {"down": true}}], "ts": "2026-10-01T00:00:20.000Z"}
{"event": "done", "success": true, "steps": 6, "final_result": "{...}", "ts": "..."}
```

- `actions` is a list of one-key objects; the key is the action name (`click`, `input`, `scroll`,
  `wait`, `navigate`, `extract`, `find_elements`, `switch`, `search_page`, `evaluate`, `done`).
- `ts` was added for this tool. Without it a trace says what the agent did but not where the run went,
  so `step_stats` reports per-step latency only for traces that carry it and says so when they do not.
- A trace that exists but has no `step` events is a real outcome — the agent died before its first
  step. It is stored as an empty cache file, which is how "fetched and empty" stays distinguishable
  from "never fetched".

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `TRACE_SOURCE` | `local` | `local` or `remote` |
| `JOBS_DB` | `<repo>/jobs.db` | Local database |
| `JOBS_DATA` | `<repo>/data` | Local traces and logs |
| `JOBS_API` | `https://jobapp.siggy-lab.org` | API base for `remote` |
| `TRACE_CACHE` | `<repo>/.tracecache` | Where fetched traces and logs are cached |

## Tests

```bash
python3 test_tracekit.py
```

32 tests, stdlib `unittest`, no network and no real database: the local source runs against a
temporary SQLite file, and the remote fetch path runs against a canned HTTP layer so the real parsing,
log handling and cache-write stay in the path under test.

## Known limits

- **The API caps `limit` at 100**, so the runs snapshot is paged (up to 4000 runs).
- **A remote run's company is not in the API's run rows.** It is recovered from the run's own log
  (`company=<slug>`) when that log is cached, so a company filter is only as complete as the sync;
  unsynced runs are honestly excluded rather than guessed at.
- **Per-step latency needs `ts`**, so it is unavailable on traces written before the writer emitted
  it. `step_stats` reports an empty latency block and explains why rather than implying zero.
- **`sync_traces` fetches at most `max_runs` (default 100) per call**, so a broad filter cannot block;
  the result says when it capped.
