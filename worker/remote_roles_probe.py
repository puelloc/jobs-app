#!/usr/bin/env python3
"""remote_roles_probe.py: one guided browser-use run to answer one question.

Given a company's first-party careers URL, drive a real *local* browser and report whether the
company has remote software-engineering roles open right now. This is the pilot that decides the
architecture for the listings workstream (docs/scraping-plan.md): does a *guided* local Ollama agent
do the "first-party page -> one hop -> listings board -> remote SWE?" flow reliably?

Environment handling mirrors worker/browser_worker.py (all three sandbox traps):
  * BROWSER_USE_CONFIG_DIR is set before browser-use is imported.
  * browser-use spawns Chromium itself, so BrowserProfile gets Playwright's executable_path plus
    --no-sandbox --disable-gpu (the outer DSH sandbox blocks Chromium's own sandbox).
  * The Chromium path is resolved before asyncio.run() (sync Playwright cannot run in the loop).
  * ChatOllama takes `host` (not base_url), and num_ctx/temperature live under `ollama_options`.

The agent is GUIDED, not free-range (see the open-source skill's agent.md "Prompting Guide"):
numbered steps, named actions, an explicit fallback branch, a structural `allowed_domains` guard, and
`output_model_schema` so the answer is structured data, not prose.

Usage:
    export OLLAMA_HOST=https://ai.siggy-lab.org
    ./worker/remote_roles_probe.py --url https://www.twilio.com/en-us/careers --company Twilio
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
import time
from pathlib import Path
from urllib.parse import urlparse

_REPO_ROOT = Path(__file__).resolve().parent.parent

# Must precede any browser_use import (see worker/browser_worker.py).
os.environ.setdefault("BROWSER_USE_CONFIG_DIR", str(_REPO_ROOT / ".browseruse"))
os.environ.setdefault("BROWSER_USE_HEADLESS", "true")
os.environ.setdefault("ANONYMIZED_TELEMETRY", "false")
os.environ.setdefault("BROWSER_USE_VERSION_CHECK", "false")

DEFAULT_OLLAMA_HOST = "https://ai.siggy-lab.org"
DEFAULT_MODEL = "qwen3.8-27b-64k:latest"
NUM_CTX = 32768
MAX_ACTIONS_PER_STEP = 3

# Where the "one hop" from a first-party careers page may land. The pattern `*.example.com` matches
# the main domain and every subdomain; bare hosts are exact matches. Navigation-only: the board's own
# XHR/JSON still flows, only page.goto is gated.
ATS_DOMAINS = [
    "*.myworkdayjobs.com",
    "*.workday.com",
    "*.successfactors.com",
    "*.sapsf.com",
    "*.icims.com",
    "*.phenom.com",
    "*.phenompeople.com",
    "*.taleo.net",
    "*.eightfold.ai",
    "*.oraclecloud.com",
    "*.smartrecruiters.com",
    "*.jobvite.com",
    "boards.greenhouse.io",
    "jobs.lever.co",
    "jobs.ashbyhq.com",
]

SYSTEM_EXTRA = (
    "You are finding job openings on a company's careers site. Prefer the company's own job search "
    "page over navigating away. Gather concrete evidence before answering: read actual job titles "
    "and locations from the page; never invent a listing you did not see. Never apply, log in, or "
    "create an account."
)


def chromium_path() -> str:
    """Absolute path to the Chromium browser-use should launch (resolved outside the event loop)."""
    override = os.environ.get("BROWSER_USE_CHROMIUM_PATH", "").strip()
    if override:
        return override
    from playwright.sync_api import sync_playwright

    with sync_playwright() as pw:
        return pw.chromium.executable_path


def browser_args() -> list[str]:
    raw = os.environ.get("BROWSER_USE_BROWSER_ARGS", "").strip()
    if raw:
        return raw.split()
    # Default: the outer sandbox blocks Chromium's own sandbox.
    return ["--no-sandbox", "--disable-gpu"]


def build_allowed_domains(url: str) -> list[str]:
    """Allow the company's own domain (+subdomains) and the known ATS boards, nothing else."""
    host = (urlparse(url).hostname or "").lower()
    if not host:
        return ATS_DOMAINS
    parts = host.split(".")
    # .com-centric: for www.twilio.com / careers.usbank.com this is the registrable domain.
    base = ".".join(parts[-2:]) if len(parts) >= 2 else host
    return [f"*.{base}", base, *ATS_DOMAINS]


def build_task(company: str, url: str) -> str:
    return f"""Determine whether {company} has remote software-engineering roles open right now.

Steps:
1. Open {url}
2. If a cookie banner or consent dialog appears, dismiss it by clicking Accept, Agree, OK, or Close.
3. Find the link to the job openings or search page. Look for text like "Careers", "Jobs",
   "Open positions", "View all jobs", "Search jobs", or "Join us". Open that page.
4. On the listings page, find the search box and search for "software engineer" (or "software",
   "engineer", "developer").
5. If there is a location or "remote" filter, set it to "Remote" (or "Remote - United States" if
   that is the only remote option).
6. Look at the results and count how many open roles are remote software-engineering roles (title
   contains software/engineer/developer AND the location is remote).

Fallbacks:
- If the page is blocked, requires login, or never loads, stop and report has_remote_software_roles
  = false with evidence explaining what happened.
- If you cannot find a search box, read the visible job list and judge from the titles you see.
- If a submit button cannot be clicked, use send_keys with "Enter".

Answer using the required fields:
- listings_url: the URL of the job search/listings page you ended on
- has_remote_software_roles: true if at least one remote software-engineering role is open, else false
- remote_software_role_count: the number of such roles you counted (0 if none)
- evidence: one or two sentences naming what you saw (example titles and whether remote)

Rules:
- Stay on {company}'s own site or its applicant-tracking board (greenhouse, lever, workday,
  successfactors, icims, phenom, taleo, eightfold). Never go to Google, a job aggregator, or a news
  site. Do NOT apply to any job, do NOT log in, and do NOT create an account."""


async def run_one(url: str, company: str, host: str, model: str, max_steps: int,
                  timeout: int, use_vision: bool, domain_guard: bool, exe: str) -> dict:
    from browser_use import Agent, BrowserProfile, ChatOllama
    from pydantic import BaseModel

    class RemoteRolesAnswer(BaseModel):
        listings_url: str
        has_remote_software_roles: bool
        remote_software_role_count: int
        evidence: str

    llm = ChatOllama(
        model=model,
        host=host,
        ollama_options={"num_ctx": NUM_CTX, "temperature": 0.0},
    )

    profile_kwargs = {
        "executable_path": exe,
        "headless": True,
        "args": browser_args(),
    }
    if domain_guard:
        profile_kwargs["allowed_domains"] = build_allowed_domains(url)

    agent = Agent(
        task=build_task(company, url),
        llm=llm,
        browser_profile=BrowserProfile(**profile_kwargs),
        output_model_schema=RemoteRolesAnswer,
        use_vision=use_vision,
        max_actions_per_step=MAX_ACTIONS_PER_STEP,
        extend_system_message=SYSTEM_EXTRA,
    )

    start = time.monotonic()
    try:
        history = await asyncio.wait_for(agent.run(max_steps=max_steps), timeout=timeout)
    except asyncio.TimeoutError:
        return {"ok": False, "url": url, "company": company, "timeout": True,
                "elapsed_sec": round(time.monotonic() - start, 1)}
    except Exception as exc:  # noqa: BLE001 - reported, not re-raised
        return {"ok": False, "url": url, "company": company,
                "error": f"{type(exc).__name__}: {exc}",
                "elapsed_sec": round(time.monotonic() - start, 1)}

    answer = getattr(history, "structured_output", None)
    if answer is not None and hasattr(answer, "model_dump"):
        answer = answer.model_dump()

    final = ""
    try:
        final = str(history.final_result() or "")
    except Exception as exc:  # noqa: BLE001
        final = f"<no final result: {exc}>"

    # The built-in judge is a trajectory-consistency check, not a fact check: it can flag a right
    # answer with an unproven path, and pass a wrong answer with a confident-but-hallucinated path.
    # Capture it as its own field rather than folding it into is_successful.
    judged = False
    try:
        judged = bool(history.is_judged())
    except Exception:  # noqa: BLE001
        pass
    judgement = getattr(history, "judgement", None)
    if callable(judgement):
        try:
            judgement = judgement()
        except Exception:  # noqa: BLE001
            judgement = None
    if judgement is not None:
        if hasattr(judgement, "model_dump"):
            judgement = judgement.model_dump()
        else:
            judgement = str(judgement)

    return {
        "ok": True,
        "url": url,
        "company": company,
        "answer": answer,
        "is_successful": bool(history.is_successful()),
        "judged": judged,
        "judgement": judgement,
        "steps": int(history.number_of_steps()),
        "elapsed_sec": round(time.monotonic() - start, 1),
        "urls": list(history.urls()),
        "final_result": final,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--url", required=True)
    ap.add_argument("--company", required=True)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--host", default=os.environ.get("OLLAMA_HOST") or DEFAULT_OLLAMA_HOST)
    ap.add_argument("--max-steps", type=int, default=25)
    ap.add_argument("--timeout", type=int, default=720, help="per-site seconds")
    ap.add_argument("--vision", action="store_true", help="include screenshots (slower)")
    ap.add_argument("--no-guard", action="store_true", help="disable the allowed_domains guard")
    args = ap.parse_args()

    exe = chromium_path()  # before asyncio.run: sync Playwright cannot run inside the loop

    print(f"url={args.url}\ncompany={args.company}\nmodel={args.model}\nhost={args.host}\n"
          f"max_steps={args.max_steps} vision={args.vision} guard={not args.no_guard}",
          flush=True)

    result = asyncio.run(run_one(
        args.url, args.company, args.host, args.model, args.max_steps,
        args.timeout, args.vision, not args.no_guard, exe,
    ))
    print(json.dumps(result, ensure_ascii=False, indent=2), flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
