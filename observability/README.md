# observability: one place for every log, from every project

Loki (store) + Alloy (collector) + Grafana (query). Three containers, ~500MB–1GB of RAM, all arm64.

```bash
cd observability
cp .env.example .env      # set a Grafana password
docker compose up -d
```

- Grafana: <http://<nas>:3001>
- Loki API: <http://<nas>:3100> (what the logs MCP server queries)

## Why this shape

**Alloy reads the Docker socket**, so it collects from *every* container on the host — jobs-app,
apply-app, apply-console, Ollama, the reverse proxy, everything — with no shared network, no per-app
sidecar, and no change to any application. Start a new container and its logs appear; that is the whole
setup.

Because of that, this stack is deliberately a **separate compose file** from the applications. Merging
would buy nothing (collection does not depend on being on the same network) and would mean one shared
file to break whenever any app changes. If you want a single command anyway, Compose's `include:`
composes files without merging them:

```yaml
# ~/Projects/jobs/compose.yaml
include:
  - jobs-app/compose.yaml
  - apply-app/compose.yaml
  - observability/compose.yaml
```

## Why not Elasticsearch / OpenSearch

OpenSearch wants **1GB+ of JVM heap** before it has indexed anything — 1.5–2GB RSS in practice, plus
Dashboards. On a Pi 5 that competes directly with Ollama, which is the resource that actually matters.

Loki indexes **labels, not text**. Full-text search still works; it is done at query time over
compressed chunks. For one user producing tens of megabytes per sweep that is not a compromise, it is
the right trade — and it is the difference between a stack that fits on the Pi and one that does not.

## Which containers are collected

Alloy is told to keep only this project's own stacks:

```alloy
rule {
  source_labels = ["__meta_docker_container_label_com_docker_compose_project"]
  regex         = "(jobs|apply-app|apply-console)"
  action        = "keep"
}
```

The host runs fourteen compose projects - immich, nextcloud, homeassistant, authentik, pihole,
livechart and more - and none of them belong in this store. Shipping them costs disk, makes every
query noisier, and buries the logs actually under investigation. Widen the regex to add one;
`observability` is deliberately absent so the stack does not log about itself into itself (Grafana
alone emits ~1,400 lines an hour).

Because the rule matches on the compose project label, a container not managed by compose is dropped
too. That is the intended reading of "only my apps".

## What makes it useful rather than just present

The collector is only half of it. `config.alloy` parses the JSON the apps emit so `trace_id`, `run_id`,
`company` and friends become queryable **fields**, and promotes only `level`, `app` and `svc` to index
**labels**. That split is deliberate: labels are an index, and making `run_id` or `company` a label
would create one stream per company and destroy the reason Loki is cheap.

So this works:

```logql
{app=~".+"} | json | trace_id = "9f2c1d4e5a6b7c8d9e0f1a2b3c4d5e6f"     # one unit of work
{app="jobs-app"} | json | company = "abbott-laboratories"               # one company, ever
{svc="batch"} | json | duration_ms > 600000                             # companies that took >10 min
{level="error"} | json | line_format "{{.svc}} {{.msg}} {{.error}}"     # what is failing
```

## Wiring the applications

Nothing is required for Docker logs. For the correlation to be worth anything, applications should emit
one JSON object per line on **stderr**, with these fields (jobs-app's `internal/logging` does this, and
is the reference implementation):

```json
{"ts":"2026-10-04T01:45:26.325Z","level":"info","app":"jobs-app","svc":"scrape",
 "msg":"summary","trace_id":"9f2c…","span":"summary","sweep_id":457,"run_id":615,
 "company":"abbott-laboratories","found":3,"inserted":1}
```

| Field | Meaning |
| --- | --- |
| `app` | `jobs-app`, `apply-app`, `apply-console` — the project |
| `svc` | the process within it (`server`, `scrape`, `batch`, `worker`, `api`) |
| `trace_id` | the unit of work, propagated across processes and services |
| `span` | the phase (`agent`, `fetch`, `store`) — what turns lines into a timeline |
| `sweep_id`, `run_id`, `company`, `listing_id`, `application_id`, `console_run_id` | the pipeline's own keys, carried as fields |

**Human-readable reports stay on stdout.** They are searchable too, just unparsed, and keeping
telemetry off stdout means nothing that already reads those files or asserts on those lines has to
change.

Propagation, all three hops:

1. The server gives every HTTP request a `trace_id` (adopting `traceparent` or `X-Trace-Id` if the
   caller sent one) and echoes it back.
2. A triggered job inherits it through the environment (`JOBS_TRACE_ID`), so the sweep it launches joins
   the same trace as the click that caused it.
3. Each company the sweep runs inherits it again, so a per-company failure joins the sweep.

The payoff is one query: `trace_timeline` in the [logs MCP](../tools/logmcp/README.md) replays a whole
chain — request → sweep → company → agent → failure — as a single ordered story, across processes that
never knew about each other.

## Operational notes

- **Disk**: Loki keeps 90 days (`limits_config.retention_period`). Docker's own logs are capped at
  10MB × 3 per container in this file; do the same for the application composes or the host fills up.
- **Security**: Loki has **no authentication**. Port 3100 is published so the MCP server can reach it,
  which is fine on the LAN and not fine on the internet. See *Exposing Loki through Nginx Proxy
  Manager* below if the agent runs on another machine.

## Sharing a network with the app stacks

Loki joins an external network (`siggy-net` by default) that the application stacks also join, so any
container in the project can reach it as **`http://loki:3100`** — by name, not by guessing an address.

```bash
docker network create siggy-net      # once on the host; jobs-app's deploy.sh does this for you
```

Then redeploy both stacks. This is worth preferring over a host-gateway address like `172.17.0.1` for
one reason: **the gateway differs per network and moves.** Each compose project gets its own subnet, so
the host is at a different address from each one, and Docker reassigns those subnets after restarts — an
address that resolves today can be wrong tomorrow. A service name cannot.

(On a single host, `172.17.0.1:<port>` often does work for a published port, because the packet is
routed to the host and delivered to the socket. It is not wrong so much as fragile, and it is
per-network: `172.17.0.1` is the *default* bridge's gateway, which is not where a compose project
usually lives.)

To give Nginx Proxy Manager name-based access too, attach it to the same network and forward to
`http://loki:3100`:

```bash
docker network connect siggy-net <npm-container>    # re-run if NPM is ever recreated
```

Otherwise point NPM at the NAS's LAN IP, which needs no network change at all.

## Exposing Loki through Nginx Proxy Manager

If the agent lives on a different machine from the NAS — or you want a hostname and TLS rather than a
bare IP — add a proxy host exactly as you did for the apps:

| NPM field | Value |
| --- | --- |
| Domain names | `loki.<your-domain>` |
| Scheme | `http` |
| Forward hostname / IP | the address NPM uses to reach this stack (the NAS's LAN IP, or `loki` if NPM shares a Docker network with it) |
| Forward port | `3100` |
| Websockets support | not needed |
| **Access List** | **required — see below** |
| SSL | request a certificate as usual |

**Do add an Access List.** Loki's API is open on both sides: the query endpoint would let anyone read
every log line you have, and the push endpoint would let them write false ones or fill the disk. Create
an Access List (HTTP Basic), attach it to the proxy host, and give the MCP server the same credentials:

```yaml
env:
  LOKI_URL: https://loki.<your-domain>
  LOKI_USERNAME: <access-list user>
  LOKI_PASSWORD: <access-list password>
```

If NPM runs in Docker and cannot reach `3100` on the host — common when it is on its own bridge
network — either use the NAS's LAN IP as the forward hostname, or attach this stack to NPM's network so
`http://loki:3100` resolves. The LAN IP is the simpler of the two and needs no compose change.

A proxy is **optional**: if the agent runs on the same LAN, `LOKI_URL: http://<nas-ip>:3100` works with
no proxy and no credentials. Add the proxy when you want a name, TLS, or access from outside the LAN.
- **Alerts worth adding first** (Grafana → Alerting), each keyed on a label rather than a message:
  `sum by (app,svc) (count_over_time({level="error"}[15m])) > 0` for any error at all;
  a restart detector (`count_over_time({msg="server starting"}[1h]) > 1`), which is the failure that
  left the UI unhealthy for four days without anyone noticing; and
  `{svc="batch"} | json | stopped_early = true`, a sweep that gave up.
- **Image tags are pinned** on purpose. An observability stack that silently upgrades itself is its own
  outage; bump them deliberately.

## Verifying the collector

```bash
# Is anything arriving?
curl -s 'http://<nas>:3100/loki/api/v1/label/app/values' | python3 -m json.tool

# What does a container's line look like once parsed?
curl -s --get 'http://<nas>:3100/loki/api/v1/query_range' \
  --data-urlencode 'query={app="jobs-app"} | json' --data-urlencode 'limit=5' | python3 -m json.tool
```
