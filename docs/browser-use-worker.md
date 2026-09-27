# browser-use worker contract (M3)

The Go orchestrator never drives a browser itself. It writes one JSON request, starts one Python
process, and reads exactly one JSON object back. Everything the worker decides is recorded, and
nothing it says is trusted: a returned URL is fed through `internal/careers.Validate` before any row
is written, exactly like a candidate from a deterministic tier.

design: `docs/sp1500-plan.md`, sections 7.2 and 7.5.

## Why a subprocess and not a library

browser-use is Python, the rest of the project is cgo-free Go that cross-compiles to the NAS. The
plan defers browser-use for the same reason: it is nondeterministic, so it cannot sit on the
primary tested path — only the seam where its output is consumed can be pinned by tests. That seam
is this contract.

## Invocation

```
python worker/browser_worker.py            # request JSON on stdin
python worker/browser_worker.py '<json>'   # request JSON as argv[1]
```

One request, one response, one process. The worker writes **exactly one line-delimited JSON object**
to stdout and nothing else; all logging, warnings and framework noise go to stderr. The Go side
redirects the worker's stdout during a call and decodes a single object, so any extra stdout output
is a protocol error rather than something to tolerate.

Exit code is `0` whenever a response object was produced, **including `ok: false`**. A response
describes what happened to the request; only a crash, a missing interpreter or an unhandled
exception exits non-zero. This keeps "the site refused us" (`ok: false`) distinct from "the worker
broke" (non-zero exit), which is the same distinction the fetcher contract makes between a 403 and a
transport error.

## Request

| Field | Type | Required | Meaning |
| --- | --- | --- | --- |
| `mode` | `"render"` \| `"agent"` | yes | `render` loads the URL in a real browser; `agent` runs the LLM navigation loop. |
| `url` | string | yes | Absolute http(s) URL to open. |
| `company_name` | string | no | Used in the agent task text and for logging. Never used to accept a page. |
| `timeout_seconds` | number | no (default 45) | Page navigation and wait budget. |
| `max_body_bytes` | integer | no (default 1500000) | Bound on the returned rendered HTML. |
| `agent` | object | for `agent` | `{model, host, max_steps, num_ctx, keep_alive}`. `host` is the Ollama base URL (`ChatOllama(host=…)`); it is not named `base_url` because browser-use's own keyword is `host` and a rename here would be one more mapping to get wrong. |

No `user_agent` field. A real browser sends its own user agent; the project's rule against
User-Agent spoofing (see the `Fetcher` contract in `internal/careers/resolve.go`) is not weakened
because the client happens to be Chromium.

## Response

Success:

```json
{
  "ok": true,
  "mode": "render",
  "requested_url": "https://example.com/careers",
  "final_url": "https://example.com/company/careers",
  "status": 200,
  "content_type": "text/html; charset=utf-8",
  "title": "Careers | Example",
  "body": "<!doctype html>…",
  "body_bytes": 481203,
  "truncated": false,
  "redirected": true,
  "note": ""
}
```

Failure — still exit 0:

```json
{
  "ok": false,
  "mode": "render",
  "requested_url": "https://example.com/careers",
  "status": 0,
  "error": "net::ERR_NAME_NOT_RESOLVED at https://example.com/careers",
  "error_kind": "navigation"
}
```

`error_kind` is one of:

| Kind | Meaning | How the orchestrator treats it |
| --- | --- | --- |
| `config` | Bad or missing request field. | Run-level failure: a bug in the caller, not a fact about the site. |
| `launch` | Chromium could not start. | Run-level failure: the environment is wrong, every URL would fail the same way. |
| `navigation` | The page could not be loaded (DNS, TLS, refused, HTTP error raised by `goto`). | `transport_error` attempt; the site is unknown, and an escalation is worth trying. |
| `timeout` | The navigation budget expired. | `timeout` attempt; retryable but not escalated by default. |
| `agent` | The LLM loop failed (import error, model unreachable, step budget exhausted). | The attempt it was escalating is recorded as unverifiable; escalation is not treated as a run failure. |
| `protocol` | The worker produced no usable response. | Run-level failure. |

## What the worker does not do

- **It does not decide whether a page is a careers page.** It reports what the browser saw. The
  verdict is Go's, produced by the same `careers.Validate` gate the deterministic tiers use.
- **It does not follow the agent's own HTML.** `agent` mode returns a URL; Go re-renders that URL in
  `render` mode and gates the result. Trusting the agent's page content would put an LLM inside the
  decision the gate exists to make.
- **It does not apply to anything, fill a form, or log in.** The task text explicitly forbids
  interacting with applications.

## Environment

| Variable | Why |
| --- | --- |
| `BROWSER_USE_CONFIG_DIR` | browser-use creates this directory on import. Without it set to a writable path it tries `~/.config/browseruse` and fails under a sandboxed run. The worker sets its own default to `<repo>/.browseruse`. |
| `BROWSER_USE_CHROMIUM_PATH` | Optional override for the browser binary. The worker otherwise resolves Playwright's own Chromium, which is what keeps the pinned Playwright version authoritative. browser-use does not use Playwright's launcher; it spawns the browser over CDP and, when its own scan for a binary misses, shells out to `uvx playwright install chromium` and then fails to find the browser anyway. Passing the path skips that. |
| `BROWSER_USE_BROWSER_ARGS` | Optional whitespace-separated Chromium flags for the agent's browser, e.g. `--no-sandbox --disable-gpu`. Unset means Chromium keeps its own sandbox, which is the default a security boundary deserves. |
| `PLAYWRIGHT_BROWSERS_PATH` | Optional. Defaults to the shared ms-playwright cache, which the pinned Playwright version already matches. |

### The sandbox fallback

Chromium's own OS sandbox cannot start inside some outer sandboxes - the DSH file sandbox is one -
and the symptom is a launch or CDP failure, not a site outcome. When the agent's first attempt fails
with a message naming the sandbox, the worker logs that it is retrying and makes **one** second
attempt with `--no-sandbox --disable-gpu`. It does not retry for any other error, it does not weaken
the browser sandbox by default, and the retry is written to stderr so it is never silent. Setting
`BROWSER_USE_BROWSER_ARGS` makes the choice deliberate instead and disables the fallback.

`render` mode is unaffected: Playwright's own launcher works without the flag.

Pinned in `worker/requirements.txt`: `playwright==1.58.0`, `browser-use==0.13.10`. The Playwright pin
is load-bearing — 1.58.0 is the version whose Chromium revision (1208) is already on disk, so the
worker never downloads a browser.
