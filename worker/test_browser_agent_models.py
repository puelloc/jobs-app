#!/usr/bin/env python3
"""Compare local Ollama models on the same browser-use task.

Adapted from a generic harness for THIS repo's environment. The three traps in
docs/browser-use-worker.md are all handled here, so don't "simplify" any of them away:

  * BROWSER_USE_CONFIG_DIR is set before browser-use is imported, otherwise importing it
    writes ~/.config/browseruse (denied by the sandbox) and dies in an unrelated pydantic
    traceback.
  * ChatOllama takes `host` (not `base_url`), and in browser-use 0.13.10 num_ctx/temperature
    are NOT top-level kwargs - they live under `ollama_options`.
  * browser-use does not use Playwright's launcher, so BrowserProfile gets the Chromium path
    Playwright resolved, plus --no-sandbox --disable-gpu because the outer DSH sandbox blocks
    Chromium's own sandbox. The path is resolved before asyncio.run() because Playwright's
    sync API refuses to start inside a running event loop.

use_vision=False keeps the mixed set (vision + non-vision models) on the same footing.
Models run sequentially: the 27b models are ~17GB each and two at once can OOM the host.

Usage:
    ./worker/test_browser_agent_models.py                          # full default run
    ./worker/test_browser_agent_models.py --models granite4.1:8b --max-steps 5  # smoke test
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
import time
from pathlib import Path

_REPO_ROOT = Path(__file__).resolve().parent.parent

# Must precede any browser_use import (see worker/browser_worker.py).
os.environ.setdefault("BROWSER_USE_CONFIG_DIR", str(_REPO_ROOT / ".browseruse"))
os.environ.setdefault("BROWSER_USE_HEADLESS", "true")
os.environ.setdefault("ANONYMIZED_TELEMETRY", "false")
os.environ.setdefault("BROWSER_USE_VERSION_CHECK", "false")

DEFAULT_OLLAMA_HOST = "https://ai.siggy-lab.org"

TASK = (
    "Go to https://en.wikipedia.org and find the height of the Eiffel Tower. "
    "Then go to https://en.wikipedia.org again and find the height of the "
    "Statue of Liberty (including pedestal). "
    "Tell me which one is taller and by how many meters."
)

# The three workhorses the scraping workstream actually uses, plus three weaker models to expose
# where a small model breaks down (bad tool calls, losing the task, hallucinated numbers).
MODELS = [
    "qwen3.8-27b-64k:latest",
    "qwen3.8-27b-120k:latest",
    "batiai/qwen3.8-27b:q3",
    "qwen3:8b",
    "qwen3.5:9b",
    "granite4.1:8b",
]

# Uniform 32k so every model gets the same budget and none trips Ollama's num_ctx ceiling
# (qwen3:8b tops out at 40960). Enough for a ~20-step Wikipedia task with message compaction.
NUM_CTX = 32768
MAX_STEPS = 20
MAX_ACTIONS_PER_STEP = 3
PER_MODEL_TIMEOUT = 15 * 60  # seconds


def chromium_path() -> str:
    """Absolute path to the Chromium browser-use should launch.

    Resolved before any event loop starts (see worker/browser_worker.py ``_chromium_path``).
    """
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
    # Default: the outer sandbox blocks Chromium's own sandbox; make the workaround explicit.
    return ["--no-sandbox", "--disable-gpu"]


async def run_one(model: str, host: str, exe: str, max_steps: int, timeout: int) -> dict:
    from browser_use import Agent, BrowserProfile, ChatOllama

    llm = ChatOllama(
        model=model,
        host=host,
        ollama_options={
            "num_ctx": NUM_CTX,
            "temperature": 0.0,
        },
    )
    agent = Agent(
        task=TASK,
        llm=llm,
        browser_profile=BrowserProfile(
            executable_path=exe,
            headless=True,
            args=browser_args(),
        ),
        use_vision=False,
        max_actions_per_step=MAX_ACTIONS_PER_STEP,
    )

    start = time.monotonic()
    try:
        history = await asyncio.wait_for(agent.run(max_steps=max_steps), timeout=timeout)
    except asyncio.TimeoutError:
        return {
            "model": model,
            "success": False,
            "timeout": True,
            "elapsed_sec": round(time.monotonic() - start, 1),
            "final_result": "",
        }
    except Exception as exc:  # noqa: BLE001 - reported, not re-raised
        return {
            "model": model,
            "success": False,
            "error": f"{type(exc).__name__}: {exc}",
            "elapsed_sec": round(time.monotonic() - start, 1),
            "final_result": "",
        }

    final = ""
    try:
        final = str(history.final_result() or "")
    except Exception as exc:  # noqa: BLE001
        final = f"<no final result: {exc}>"

    return {
        "model": model,
        "success": bool(history.is_successful()),
        "is_done": bool(history.is_done()),
        "steps": int(history.number_of_steps()),
        "elapsed_sec": round(time.monotonic() - start, 1),
        "duration_sec": round(float(history.total_duration_seconds()), 1),
        "has_errors": bool(history.has_errors()),
        "urls": list(history.urls()),
        "final_result": final,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--models", default=",".join(MODELS), help="comma-separated model list")
    parser.add_argument("--host", default=os.environ.get("OLLAMA_HOST") or DEFAULT_OLLAMA_HOST)
    parser.add_argument("--max-steps", type=int, default=MAX_STEPS)
    parser.add_argument("--timeout", type=int, default=PER_MODEL_TIMEOUT, help="per-model seconds")
    parser.add_argument("--out", default=str(_REPO_ROOT / "worker" / "model_compare_results.json"))
    args = parser.parse_args()

    models = [m.strip() for m in args.models.split(",") if m.strip()]
    exe = chromium_path()  # before asyncio.run: sync Playwright cannot run inside the loop

    print(f"host={args.host}  models={len(models)}  num_ctx={NUM_CTX}  "
          f"max_steps={args.max_steps}  per-model-timeout={args.timeout}s", flush=True)

    async def _run_all() -> list[dict]:
        results: list[dict] = []
        for model in models:
            print(f"\n=== Testing {model} ===", flush=True)
            r = await run_one(model, args.host, exe, args.max_steps, args.timeout)
            results.append(r)
            print(json.dumps(r, ensure_ascii=False), flush=True)
        return results

    results = asyncio.run(_run_all())

    print("\n\n=== SUMMARY ===", flush=True)
    for r in results:
        if r.get("success"):
            status = "OK"
        elif r.get("timeout"):
            status = "TIMEOUT"
        else:
            status = "FAILED"
        ans = (r.get("final_result") or r.get("error") or "").strip().replace("\n", " ")
        print(f"{r['model']:<28} {status:<8} {r.get('elapsed_sec', 0):>6}s  "
              f"steps={r.get('steps', '')}  {ans[:180]}", flush=True)

    out = Path(args.out)
    out.write_text(json.dumps(results, ensure_ascii=False, indent=2))
    print(f"\nResults written to {out}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
