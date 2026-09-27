#!/usr/bin/env python3
"""browser-use worker: one JSON request in, exactly one JSON object out.

The Go orchestrator owns every decision. This process only reports what a real browser saw
(``render``) or where an LLM-driven navigation loop ended up (``agent``). The URL it returns is
re-validated by ``internal/careers.Validate`` before anything is written, so nothing here is trusted.

Contract: docs/browser-use-worker.md. Keep the two in step; the Go side is
``internal/browseruse``.

Design notes that are load-bearing:
  * stdout carries exactly one JSON object and nothing else. Libraries print to stdout freely
    (progress bars, banners, model output), so stdout is redirected to stderr for the whole run and
    the response is written to file descriptor 1 directly at the end. A stray print would otherwise
    corrupt the protocol, and a protocol error is indistinguishable from a broken worker.
  * Exit code 0 means "a response object was produced", including ``ok: false``. The site refusing
    us and the worker breaking are different facts and must not collapse into one exit code.
  * ``agent`` mode returns a URL, never HTML. Reporting the agent's page content would let the LLM
    inside the validation decision; Go re-renders the URL and gates that instead.
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path
from typing import Any

# browser-use creates its config directory at import time. Left alone it is ~/.config/browseruse,
# which a sandboxed run cannot write, and the failure surfaces as an unrelated pydantic traceback.
_REPO_ROOT = Path(__file__).resolve().parent.parent
os.environ.setdefault("BROWSER_USE_CONFIG_DIR", str(_REPO_ROOT / ".browseruse"))
os.environ.setdefault("BROWSER_USE_HEADLESS", "true")
# The worker is a batch tool on a private network; telemetry and update checks are noise.
os.environ.setdefault("ANONYMIZED_TELEMETRY", "false")
os.environ.setdefault("BROWSER_USE_VERSION_CHECK", "false")

DEFAULT_TIMEOUT_SECONDS = 45.0
DEFAULT_MAX_BODY_BYTES = 1_500_000
DEFAULT_MAX_STEPS = 8
DEFAULT_MODEL = "qwen3.8-27b-64k:latest"


def _respond(payload: dict[str, Any]) -> None:
    """Write the single response object to fd 1, bypassing any redirected sys.stdout."""
    data = json.dumps(payload, ensure_ascii=False).encode("utf-8", "replace")
    os.write(1, data + b"\n")


def _failure(mode: str, url: str, kind: str, message: str, status: int = 0) -> dict[str, Any]:
    return {
        "ok": False,
        "mode": mode,
        "requested_url": url,
        "status": status,
        "error": message,
        "error_kind": kind,
    }


def _truncate(body: str, max_bytes: int) -> tuple[str, bool]:
    raw = body.encode("utf-8", "replace")
    if len(raw) <= max_bytes:
        return body, False
    return raw[:max_bytes].decode("utf-8", "ignore"), True


def render(request: dict[str, Any]) -> dict[str, Any]:
    url = request["url"]
    timeout_seconds = float(request.get("timeout_seconds") or DEFAULT_TIMEOUT_SECONDS)
    timeout_ms = max(1000, int(timeout_seconds * 1000))
    max_bytes = int(request.get("max_body_bytes") or DEFAULT_MAX_BODY_BYTES)

    from playwright.sync_api import Error as PlaywrightError
    from playwright.sync_api import TimeoutError as PlaywrightTimeoutError
    from playwright.sync_api import sync_playwright

    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            try:
                context = browser.new_context()
                page = context.new_page()
                page.set_default_timeout(timeout_ms)
                try:
                    response = page.goto(url, wait_until="domcontentloaded", timeout=timeout_ms)
                except PlaywrightTimeoutError as exc:
                    return _failure("render", url, "timeout", f"navigation budget expired: {exc}")
                except PlaywrightError as exc:
                    return _failure("render", url, "navigation", str(exc))

                # A job board often paints its list after the document is ready. This is a bonus,
                # never a requirement: a page that never goes idle is still a page we read.
                try:
                    page.wait_for_load_state("networkidle", timeout=min(8000, timeout_ms))
                except PlaywrightError:
                    pass

                if response is None:
                    return _failure("render", url, "navigation", f"no response object for {url}")

                final_url = page.url
                body, truncated = _truncate(page.content(), max_bytes)
                return {
                    "ok": True,
                    "mode": "render",
                    "requested_url": url,
                    "final_url": final_url,
                    "status": int(response.status),
                    "content_type": response.headers.get("content-type", ""),
                    "title": page.title(),
                    "body": body,
                    "body_bytes": len(body.encode("utf-8", "replace")),
                    "truncated": truncated,
                    "redirected": final_url.rstrip("/") != url.rstrip("/"),
                    "note": "",
                }
            finally:
                browser.close()
    except PlaywrightError as exc:
        # Launch failure: no browser at all. Every URL would fail identically, so this is an
        # environment fault rather than a fact about the site.
        return _failure("render", url, "launch", str(exc))


def _chromium_path() -> str:
    """Absolute path to the Chromium browser-use should launch.

    browser-use does not use Playwright's own launcher: it spawns the browser itself over CDP, and
    to find a binary it scans a few fallback locations. When that scan misses - which it does on
    macOS, where Playwright's build lives under ``~/Library/Caches/ms-playwright`` - it runs
    ``uvx playwright install chromium`` in a throwaway environment and then fails to find the
    browser anyway. Handing it the path Playwright already resolved skips that entirely and keeps
    the pinned Playwright version and its cached browser as the single source of truth.

    Resolved outside any event loop: Playwright's sync API refuses to start inside one.
    """
    override = os.environ.get("BROWSER_USE_CHROMIUM_PATH", "").strip()
    if override:
        return override
    from playwright.sync_api import sync_playwright

    with sync_playwright() as pw:
        return pw.chromium.executable_path


def _browser_args() -> list[str] | None:
    """Extra Chromium flags for the agent's browser, from BROWSER_USE_BROWSER_ARGS.

    Unset means "let Chromium use its own sandbox", which is the default a security boundary
    deserves. The one retry in run_agent is the escape hatch for an outer sandbox that makes that
    impossible; this variable is how an operator makes the choice deliberate instead.
    """
    raw = os.environ.get("BROWSER_USE_BROWSER_ARGS", "").strip()
    if not raw:
        return None
    return raw.split()


def _looks_like_sandbox_failure(exc: BaseException) -> bool:
    message = str(exc).lower()
    return "sandbox" in message or "cdp connection" in message or "devtools" in message


def _agent_task(url: str, company_name: str) -> str:
    company = company_name.strip() or "this company"
    return (
        f"Check whether a company careers page is reachable and correct for {company}.\n"
        f"1. Open {url}\n"
        f"2. Decide whether this page lists open jobs at {company}.\n"
        f"3. If it does not, look for a link on the same site whose text is like Careers, Jobs, "
        f"Open positions or Join us, and open it. Stay on {company}'s own site or its "
        f"applicant-tracking board. Never go to a job aggregator, a news article, a product page or "
        f"an investor-relations page.\n"
        f"Do not fill in an application, do not log in, and never click Apply.\n"
        f"Report the URL you ended on, whether it lists jobs, and how many job listings are visible."
    )


def run_agent(request: dict[str, Any]) -> dict[str, Any]:
    url = request["url"]
    options = request.get("agent") or {}

    # Imported here rather than at module scope so a broken browser-use install only breaks the
    # escalation path, not the render sweep that does the bulk of the work.
    try:
        import asyncio

        from browser_use import Agent, BrowserProfile, ChatOllama
        from pydantic import BaseModel
    except Exception as exc:  # noqa: BLE001 - reported, not handled
        return _failure("agent", url, "agent", f"browser-use is unavailable: {exc}")

    # Resolved here, before asyncio.run, because the sync Playwright API used to find the browser
    # cannot be entered from inside a running event loop.
    try:
        executable_path = _chromium_path()
    except Exception as exc:  # noqa: BLE001 - reported, not handled
        return _failure("agent", url, "launch", f"could not locate Chromium: {exc}")

    class AgentAnswer(BaseModel):
        final_url: str
        is_job_listings_page: bool
        job_count: int
        reasoning: str

    llm_kwargs: dict[str, Any] = {
        "model": options.get("model") or DEFAULT_MODEL,
    }
    host = (options.get("host") or "").strip()
    if host:
        llm_kwargs["host"] = host
    ollama_options: dict[str, Any] = {"keep_alive": options.get("keep_alive", -1)}
    if options.get("num_ctx"):
        ollama_options["num_ctx"] = int(options["num_ctx"])
    llm_kwargs["ollama_options"] = ollama_options

    max_steps = int(options.get("max_steps") or DEFAULT_MAX_STEPS)

    # Live trace: when the caller asks for a trace file, append one JSON object per step (thought,
    # next goal, actions) plus a final "done" object, so a long agent run can be watched as it
    # happens. Tracing is best-effort: a trace that cannot be written never breaks the worker, and
    # the one-process-per-request model means the OS closes the file when the process exits.
    trace_file = str(request.get("trace_file") or "").strip()
    trace_fh = None
    if trace_file:
        try:
            trace_fh = open(trace_file, "a", encoding="utf-8")
        except OSError as exc:  # noqa: BLE001 - reported, not raised
            print(f"browser_worker: cannot open trace file {trace_file}: {exc}", file=sys.stderr)
            trace_fh = None

    def _emit_trace(event: dict) -> None:
        if trace_fh is None:
            return
        try:
            trace_fh.write(json.dumps(event, ensure_ascii=False, default=str) + "\n")
            trace_fh.flush()
        except OSError:
            pass  # tracing must never break the worker

    def _action_summary(action) -> dict:
        try:
            d = action.model_dump(exclude_none=True)
        except Exception:  # noqa: BLE001
            d = {"raw": str(action)}
        if isinstance(d, dict):
            d.pop("interacted_element", None)
        return d

    def _on_step(state, output, step: int) -> None:
        actions = getattr(output, "action", None) or []
        if not isinstance(actions, (list, tuple)):
            actions = [actions]
        _emit_trace({
            "event": "step",
            "step": step,
            "url": getattr(state, "url", ""),
            "thinking": getattr(output, "thinking", None),
            "evaluation_previous_goal": getattr(output, "evaluation_previous_goal", None),
            "memory": getattr(output, "memory", None),
            "next_goal": getattr(output, "next_goal", None),
            "actions": [_action_summary(a) for a in actions],
        })

    def _on_done(history) -> None:
        final = ""
        try:
            final = str(history.final_result() or "")
        except Exception:  # noqa: BLE001
            pass
        _emit_trace({
            "event": "done",
            "success": bool(history.is_successful()),
            "steps": int(history.number_of_steps()),
            "final_result": final,
        })

    async def _run(extra_args: list[str] | None) -> Any:
        llm = ChatOllama(**llm_kwargs)
        agent = Agent(
            task=_agent_task(url, str(request.get("company_name") or "")),
            llm=llm,
            browser_profile=BrowserProfile(
                executable_path=executable_path,
                headless=True,
                args=extra_args or [],
            ),
            output_model_schema=AgentAnswer,
            use_vision=False,
            register_new_step_callback=_on_step,
            register_done_callback=_on_done,
        )
        return await agent.run(max_steps=max_steps)

    configured_args = _browser_args()
    try:
        history = asyncio.run(_run(configured_args))
    except Exception as exc:  # noqa: BLE001 - reported to the orchestrator
        # Chromium's own OS sandbox cannot start inside some outer sandboxes (the DSH file sandbox
        # is one), and the symptom is a launch or CDP failure rather than a site outcome. Retry once
        # with the browser sandbox off, and say so on stderr: weakening a security boundary is
        # acceptable only when it is forced, once, and visible.
        if configured_args is None and _looks_like_sandbox_failure(exc):
            print(
                f"browser_worker: browser launch failed ({exc}); retrying once with "
                "--no-sandbox because the outer sandbox blocks Chromium's own sandbox",
                file=sys.stderr,
            )
            try:
                history = asyncio.run(_run(["--no-sandbox", "--disable-gpu"]))
            except Exception as retry_exc:  # noqa: BLE001 - reported to the orchestrator
                return _failure("agent", url, "agent", f"{type(retry_exc).__name__}: {retry_exc}")
        else:
            return _failure("agent", url, "agent", f"{type(exc).__name__}: {exc}")

    structured = getattr(history, "structured_output", None)
    final_url = ""
    note = ""
    if structured is not None:
        if hasattr(structured, "model_dump"):
            structured = structured.model_dump()
        final_url = str(structured.get("final_url") or "")
        note = str(structured.get("reasoning") or "")
        job_count = structured.get("job_count")
        if job_count is not None:
            note = f"jobs={job_count}; {note}"
    if not final_url:
        try:
            final_url = str(history.final_result() or "")
        except Exception:  # noqa: BLE001 - a missing final result is reported below
            final_url = ""
    if not final_url.startswith("http"):
        return _failure(
            "agent", url, "agent",
            f"the agent did not report a usable URL (got {final_url!r}); note={note}",
        )

    return {
        "ok": True,
        "mode": "agent",
        "requested_url": url,
        "final_url": final_url,
        "status": 0,
        "content_type": "",
        "title": "",
        "body": "",
        "body_bytes": 0,
        "truncated": False,
        "redirected": final_url.rstrip("/") != url.rstrip("/"),
        "note": note,
    }


def handle(request: dict[str, Any]) -> dict[str, Any]:
    mode = str(request.get("mode") or "render")
    url = str(request.get("url") or "")
    if not url.startswith("http://") and not url.startswith("https://"):
        return _failure(mode, url, "config", f"url must be absolute http(s), got {url!r}")
    if mode == "render":
        return render(request)
    if mode == "agent":
        return run_agent(request)
    return _failure(mode, url, "config", f"unknown mode {mode!r}")


def main(argv: list[str]) -> int:
    # Read the request before stdout is redirected; the request may arrive on stdin.
    if len(argv) > 1:
        raw = argv[1]
    else:
        raw = sys.stdin.read()

    # From here on stdout belongs to the protocol alone. Libraries that print go to stderr, where
    # the Go side captures them for the run log.
    sys.stdout = sys.stderr

    try:
        request = json.loads(raw)
        if not isinstance(request, dict):
            raise ValueError(f"request must be a JSON object, got {type(request).__name__}")
    except Exception as exc:  # noqa: BLE001 - reported as a protocol failure
        _respond(_failure("", "", "protocol", f"could not read the request: {exc}"))
        return 0

    try:
        _respond(handle(request))
    except Exception as exc:  # noqa: BLE001 - a response always beats a traceback on stdout
        _respond(_failure(str(request.get("mode") or ""), str(request.get("url") or ""),
                          "protocol", f"{type(exc).__name__}: {exc}"))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
