# logs MCP: query the central log store

An MCP server (plus a CLI over the same engine) that answers questions about the logs every project
ships to Loki. The sibling of [tools/tracemcp](../tracemcp/README.md): that one reads jobs-app's
structured run and trace data directly out of SQLite, this one reads the logs, which is what makes a
failure that crosses processes — server triggers a sweep, batch runs a company, the worker fails — into
one readable story.

## Why it exists

Without it, the answer to "why did this fail" is `docker logs` against four containers and a guess about
which one matters. With it, the answer is one call, because the [correlation fields](../../observability/README.md)
are already on every line.

## Tools

| Tool | What it answers |
| --- | --- |
| `status` | What is actually being collected — label names and their values. Call it first. |
| `error_summary` | What is failing, grouped by app/service/message, counted **by the store** so the totals are exact |
| `trace_timeline` | Everything that happened during one unit of work, in order, across every service |
| `search_logs` | Raw LogQL for anything the other tools do not anticipate |
| `container_health` | Per-container error *density*, so the misbehaving container stands out from the merely noisy one |

`trace_timeline` is the important one. Given the `trace_id` the server assigned to a request, it replays:

```
01:59:36  jobs-app  server  info   request
01:59:37  jobs-app  batch   info   sweep started    companies=436
01:59:38  jobs-app  scrape  info   start            span=start
02:00:37  jobs-app  scrape  info   agent decided    span=agent
02:02:32  jobs-app  scrape  error  agent failed     span=agent error="click timed out after 15s"
02:02:33  jobs-app  batch   error  company failed   error="exit status 1"
```

Six lines, three services, two processes — and the failing company's name on all of them, without
anyone having to know which container logged what.

## Registering it

### With dsh

Add to your profile's patch layer (`~/.dsh/profiles/<profile>/cordis.patch.yml`):

```yaml
- insert:
    - id: mcp-logs
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        serverName: logs
        transport: stdio
        command: python3
        args:
          - /Users/cris/Projects/jobs/jobs-app/tools/logmcp/server.py
        env:
          # Point this at wherever Loki runs. Defaults to http://127.0.0.1:3100.
          LOKI_URL: http://<nas>:3100
```

Tools appear as `mcp__logs__error_summary`, `mcp__logs__trace_timeline`, and so on. A patch entry with
`insert` and **no** `id` appends to the root entry list; an entry *with* an id would be read as an
override of an existing one and warn `patch: entry "…" not found`.

Verify before restarting:

```bash
dsh --dump-config --profile web | grep -A9 mcp-logs
```

### With any other MCP client

Standard stdio server: point the client at `python3 .../tools/logmcp/server.py` and set `LOKI_URL` in
its `env` block.

No dependencies, deliberately: it runs with whatever `python3` is on `PATH`, like its sibling.

## CLI

Dispatches through the same functions the MCP server exposes, so it is a faithful harness rather than a
second implementation that can drift:

```bash
cd tools/logmcp
python3 cli.py status
python3 cli.py errors   --window 2h
python3 cli.py timeline 9f2c1d4e5a6b7c8d9e0f1a2b3c4d5e6f --window 7d
python3 cli.py search   '{app="jobs-app"} | json | company="cisco"' --window 24h
python3 cli.py health   --window 6h
```

`--loki <url>` overrides the store for one command.

## The log format it expects

One JSON object per line on stderr. It degrades honestly rather than requiring this: a line that is not
JSON is returned as `raw` (so a Python traceback or a human report is still visible in a timeline), and a
JSON line keeps any field the engine does not know about under `fields` — which means an app can start
logging a new field without a change here.

| Field | Used for |
| --- | --- |
| `level` | filtering and `error_summary` |
| `app`, `svc` | grouping, and the labels Loki indexes |
| `msg` | the event name; `error_summary` groups by it |
| `trace_id` | `trace_timeline` (32 hex characters) |
| `span` | ordering a timeline into phases |
| `run_id`, `sweep_id`, `company` | joining a line to the pipeline's own keys |
| everything else | returned as-is under `fields` |

An identifier of `0` is treated as unset, because run and sweep ids start at 1 and reporting `run_id: 0`
would invent a run that does not exist. Zero stays meaningful for anything measurable — `duration_ms: 0`
is a fast request, not a missing one.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `LOKI_URL` | `http://127.0.0.1:3100` | The store to query |
| `LOKI_TIMEOUT` | `30` | Per-request seconds |
| `LOKI_USERNAME` / `LOKI_PASSWORD` | — | HTTP Basic, for an Nginx Proxy Manager Access List |
| `LOKI_TOKEN` | — | `Authorization: Bearer …`, if the proxy expects a token instead |

## Reaching a Loki behind a reverse proxy

Loki has **no authentication of its own**, so anything exposing it supplies one. Both common shapes
work: an **Nginx Proxy Manager Access List** (HTTP Basic) and a proxy expecting a bearer token. A token
wins if both are set.

```yaml
env:
  LOKI_URL: https://loki.example.com
  LOKI_USERNAME: <access-list user>
  LOKI_PASSWORD: <access-list password>
```

If the tools report `HTTP 401`, the credentials are missing or wrong. If they report a connection
error, the URL is unreachable from where the agent runs — a different problem, and the tool says which
by distinguishing an HTTP status from a transport failure.

Note that Loki's *push* endpoint sits behind the same proxy as its query endpoint, so an Access List
also stops anyone writing fake log lines or filling the disk. That is a good reason to add one even on
a network you trust.

## Tests

```bash
python3 test_logkit.py
```

27 tests. Most run against a canned Loki, so the suite needs no store and no network. Four run against a
**real** Loki when one is reachable (and skip themselves when it is not), because a fake proves the code
does what the test assumes and only a real store proves the assumption was right — the generated LogQL
is parsed by Loki, and one of those tests pushes a trace and reads it back to verify the correlation
design end to end.

## Known limits

- **`container_health` reads log lines**, so it sees a container *reporting* a problem, not one killed
  before it could log. True restart counts need the Docker events API, which is deliberately out of
  scope here; the `server starting` line is the honest in-log proxy and is worth alerting on.
- **`error_summary` counts what the apps chose to log at `error`.** An exception swallowed by a library
  is invisible to it, which is an argument for logging failures where they happen rather than where they
  surface.
- **`trace_timeline` needs a trace id**, so a failure that predates trace propagation — or one from an
  app that has not adopted it yet — has to be found with `search_logs` and `company`/`run_id` instead.

## Lookups by key

Five tools answer a question about one specific thing rather than a pattern across everything. They
match on the correlation fields directly, without `| json`, because those fields are **structured
metadata** in the store — so the filter is applied against the index instead of parsing every line in
the window. (A lookup written as `{msg="agent step"}` silently returns nothing: `msg` is not a label.)

| tool | question it answers |
| --- | --- |
| `run_timeline(run_id)` | one company's scrape, end to end: outcome, agent steps and seconds, phase timings, listings stored, and the whole timeline |
| `sweep_timeline(sweep_id)` | what the sweep planned, who it skipped before starting and why, and a row per company with its outcome |
| `company_history(company)` | every run for one company, newest first, plus an outcome tally — is this the same failure every time? |
| `listing_story(listing_id)` | one job across apps: the scrape that found it and every application made from it |

Each returns both a derived summary and the raw timeline, so the conclusion and the evidence arrive
together. The summary is derived the same way in all of them (`_run_facts`), so a company's outcome in a
sweep listing cannot disagree with the same company's outcome looked up on its own.

`run_timeline` is the workhorse. On a real run:

```
run_timeline(630)  outcome=no_remote_roles  steps=16  agent_s=623.2  vendor=greenhouse
```

Sixteen steps and ten minutes for one company is the kind of thing that is invisible in a run list and
obvious here.

### Keeping the query cheap

```python
# Yes: structured metadata, filtered by the store.
'{app=~".+"} | run_id = "630"'

# No: parses every line in the window to find one run, and `msg` is not a label at all.
'{app=~".+"} | json | run_id="630"'
```

## Is this server running the current code?

The MCP server is spawned by the harness and imports its modules **once**. Editing this package therefore
does nothing until that process is restarted, and nothing about the answers reveals it: the tools still
work, they just work with old code, and the only symptom is a field that should exist and does not.

`status` now reports it:

```json
"mcp": {"loaded_at": "...", "source": ".../logkit.py", "stale": false}
```

`stale: true` means the source file changed after this process loaded it - restart the harness, or the
tools will keep answering with the old behaviour. This exists because it cost two round trips to work
out by hand: the process had started **63 seconds before** the fix was written.
