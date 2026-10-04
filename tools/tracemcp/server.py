#!/usr/bin/env python3
"""trace MCP server: filter and analyse browser-use run traces, over stdio.

Speaks JSON-RPC 2.0 on stdin/stdout (newline-delimited), which is the MCP stdio transport. Written
against the stdlib rather than an SDK because this tool has to keep working: the repo's browser venv
was pinned to a Homebrew Python that a routine upgrade deleted, taking the installed MCP SDK with it.

Nothing but protocol messages may go to stdout. Diagnostics go to stderr, where the client logs them.

Run standalone for a smoke test:

    printf '%s\\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | python3 server.py
"""

from __future__ import annotations

import json
import os
import sys
import traceback

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from tracekit import Lab, TraceKitError  # noqa: E402

SERVER_NAME = "traces"
SERVER_VERSION = "0.1.0"

# The revisions the DSH MCP client negotiates. We advertise the newest and echo whatever the client
# asks for when it is one of these, which is what keeps a version skew from breaking the handshake.
SUPPORTED_PROTOCOLS = (
    "2026-07-28",
    "2025-11-25",
    "2025-06-18",
    "2025-03-26",
    "2024-11-05",
    "2024-10-07",
)
LATEST_PROTOCOL = SUPPORTED_PROTOCOLS[0]

INSTRUCTIONS = (
    "Reads the jobs-app scraper's run history and the browser-use agent traces those runs produced. "
    "Start with run_stats to see the shape of the corpus, then sync_traces to pull the traces you "
    "care about into the local cache, then trace_digest / search_steps / step_stats to work on them. "
    "Every analysis tool reads the cache, so syncing once makes later questions instant. "
    "A trace with no `step` events means the agent never completed a step; per-step latency is only "
    "available for traces written after the trace writer began emitting `ts`."
)

# ---------------------------------------------------------------- tool schemas

RUN_FILTERS: dict = {
    "platform": {
        "type": "string",
        "description": "Exact platform name, e.g. career_listings (one company), career_batch (a whole sweep).",
    },
    "status": {"type": "string", "enum": ["ok", "error", "running"]},
    "company": {
        "type": "string",
        "description": "Substring match on company slug. Known from the local DB, or filled in from a synced trace.",
    },
    "error_contains": {"type": "string", "description": "Substring match on the run's error text."},
    "min_duration_s": {"type": "number"},
    "max_duration_s": {"type": "number"},
    "since": {"type": "string", "description": "ISO 8601; keep runs started at or after this instant."},
    "until": {"type": "string", "description": "ISO 8601; keep runs started at or before this instant."},
    "min_id": {"type": "integer"},
    "max_id": {"type": "integer"},
    "has_trace": {
        "type": "boolean",
        "description": "true keeps only runs whose trace is already in the local cache.",
    },
    "sort": {
        "type": "string",
        "enum": ["id_desc", "id_asc", "duration_desc", "duration_asc", "started_desc"],
        "default": "id_desc",
    },
    "limit": {"type": "integer", "default": 50, "minimum": 1},
    "offset": {"type": "integer", "default": 0, "minimum": 0},
}

TOOLS: list[dict] = [
    {
        "name": "list_runs",
        "description": (
            "List runs matching a filter, newest first by default. Returns id, platform, status, "
            "duration, company and error text. Use this to find the runs worth investigating; use "
            "run_stats for the aggregate shape instead of paging through everything."
        ),
        "inputSchema": {"type": "object", "properties": RUN_FILTERS, "additionalProperties": False},
    },
    {
        "name": "run_stats",
        "description": (
            "Aggregate statistics for the runs matching a filter: count, duration percentiles, total "
            "wall hours, status and platform breakdown, the most common error texts, and how many "
            "have a trace cached. This is the first call to make on a question like 'where does the "
            "sweep spend its time'."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                **RUN_FILTERS,
                "with_traces": {
                    "type": "boolean",
                    "default": False,
                    "description": "Also compute step/action statistics over cached traces (slower).",
                },
            },
            "additionalProperties": False,
        },
    },
    {
        "name": "sync_traces",
        "description": (
            "Download the traces and logs for matching runs into the local cache. Idempotent: already "
            "cached traces are reused unless force is true. Call this before trace_digest, "
            "search_steps or step_stats. Returns how many were fetched, already cached, missing and "
            "failed."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                **RUN_FILTERS,
                "max_runs": {
                    "type": "integer",
                    "default": 100,
                    "description": "Cap the runs fetched in this call, so a broad filter cannot block.",
                },
                "force": {"type": "boolean", "default": False},
            },
            "additionalProperties": False,
        },
    },
    {
        "name": "trace_digest",
        "description": (
            "One run's trace, condensed: step count, distinct URLs, the action histogram, the first "
            "and last URL, whether the agent reached a final answer, and a compact per-step list with "
            "the step's goal. Use it once you have picked a specific run to understand."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "run_id": {"type": "integer"},
                "max_steps": {"type": "integer", "default": 40},
                "include_text": {
                    "type": "boolean",
                    "default": False,
                    "description": "Keep the full reasoning text rather than truncating each step's goal.",
                },
            },
            "required": ["run_id"],
            "additionalProperties": False,
        },
    },
    {
        "name": "search_steps",
        "description": (
            "Regex search across the cached traces' step fields. This is the tool for finding a "
            "pattern rather than reading traces one by one: a URL fragment, an action name, or a "
            "phrase the agent repeats when it is stuck. Returns matching runs with the step number, "
            "field and a snippet."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                **RUN_FILTERS,
                "pattern": {"type": "string", "description": "Python regular expression, case-insensitive."},
                "fields": {
                    "type": "array",
                    "items": {
                        "type": "string",
                        "enum": [
                            "url",
                            "next_goal",
                            "thinking",
                            "evaluation_previous_goal",
                            "memory",
                            "actions",
                        ],
                    },
                    "description": "Which step fields to search. Defaults to all of them.",
                },
                "limit": {"type": "integer", "default": 60},
            },
            "required": ["pattern"],
            "additionalProperties": False,
        },
    },
    {
        "name": "step_stats",
        "description": (
            "Step, action and timing statistics over the cached traces of matching runs: steps per "
            "run, the distribution across step-count buckets, the total action histogram (click, "
            "scroll, wait, navigate...), how many reached a final answer, and per-step latency with "
            "the slowest steps. This is where an optimisation question gets its evidence."
        ),
        "inputSchema": {"type": "object", "properties": RUN_FILTERS, "additionalProperties": False},
    },
    {
        "name": "sweep_outcomes",
        "description": (
            "Per-company outcomes for a whole sweep, parsed from the batch run's log: how many "
            "companies had no remote roles, how many failed, how many were blocked by robots, and how "
            "many actually reached a fetch (with the totals they found and inserted). This is the view "
            "for asking where a sweep spends its time - a trace covers one company, this covers all of "
            "them. Defaults to the newest sweep that has a log."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "batch_run_id": {
                    "type": "integer",
                    "description": "A career_batch run id. Defaults to the newest sweep with a cached log.",
                },
            },
            "additionalProperties": False,
        },
    },
    {
        "name": "get_run_log",
        "description": (
            "A run's log: the scraper's summary lines (listings=, resolution=, found=, inserted=, "
            "closed=, remote_evidence=). A per-company run during a sweep has an empty log of its own, "
            "because the child process inherits the batch's stdout - so this falls back to the lines "
            "its parent sweep captured, and reports which it used as `origin`."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "run_id": {"type": "integer"},
                "grep": {"type": "string", "description": "Optional regex; keep only matching lines."},
                "max_lines": {"type": "integer", "default": 200},
            },
            "required": ["run_id"],
            "additionalProperties": False,
        },
    },
]

TOOL_NAMES = {tool["name"] for tool in TOOLS}


# ---------------------------------------------------------------- tool implementations


def _lab() -> Lab:
    return Lab()


def _filter_kwargs(arguments: dict) -> dict:
    """Split a tool's arguments into the run-filter subset, so every tool takes the same vocabulary."""
    known = set(RUN_FILTERS)
    return {key: value for key, value in arguments.items() if key in known}


def tool_list_runs(arguments: dict) -> dict:
    lab = _lab()
    total = len(lab.query(**_filter_kwargs({**arguments, "limit": None, "offset": 0})))
    rows = lab.query(**_filter_kwargs(arguments))
    return {
        "matched": total,
        "returned": len(rows),
        "source": lab.source,
        "runs": [
            {
                "id": row["id"],
                "platform": row["platform"],
                "status": row["status"],
                "duration_s": round(row["duration_s"], 1) if row.get("duration_s") is not None else None,
                "company": row.get("company") or "",
                "started_at": row.get("started_at"),
                "error": (row.get("error_text") or "")[:120],
                "trace_cached": lab.has_trace(row["id"]),
            }
            for row in rows
        ],
    }


def tool_run_stats(arguments: dict) -> dict:
    lab = _lab()
    runs = lab.query(**_filter_kwargs({**arguments, "limit": None, "offset": 0}))
    stats = lab.stats(runs, with_traces=bool(arguments.get("with_traces")))
    stats["source"] = lab.source
    stats["cache_dir"] = lab.cache_dir
    return stats


def tool_sync_traces(arguments: dict) -> dict:
    lab = _lab()
    matched = len(lab.query(**_filter_kwargs({**arguments, "limit": None, "offset": 0})))
    runs = lab.query(**_filter_kwargs(
        {**arguments, "limit": arguments.get("max_runs") or 100, "offset": 0}
    ))
    result = lab.sync(runs, force=bool(arguments.get("force")))
    result["matched_by_filter"] = matched
    result["source"] = lab.source
    if matched > len(runs):
        result["note"] = (
            f"only {len(runs)} of {matched} matching runs were fetched; raise max_runs or narrow the filter"
        )
    return result


def tool_trace_digest(arguments: dict) -> dict:
    return _lab().digest(
        int(arguments["run_id"]),
        max_steps=int(arguments.get("max_steps") or 40),
        include_text=bool(arguments.get("include_text")),
    )


def tool_search_steps(arguments: dict) -> dict:
    from tracekit import STEP_FIELDS

    lab = _lab()
    runs = lab.query(**_filter_kwargs({**arguments, "limit": None, "offset": 0}))
    fields = tuple(arguments.get("fields") or STEP_FIELDS)
    return lab.search_steps(
        arguments["pattern"],
        runs,
        fields=fields,
        limit=int(arguments.get("limit") or 60),
    )


def tool_step_stats(arguments: dict) -> dict:
    lab = _lab()
    runs = lab.query(**_filter_kwargs({**arguments, "limit": None, "offset": 0}))
    stats = lab.step_stats(runs)
    stats["runs_matched"] = len(runs)
    stats["traces_cached"] = sum(1 for r in runs if lab.has_trace(r["id"]))
    stats["cache_dir"] = lab.cache_dir
    return stats


def tool_get_run_log(arguments: dict) -> dict:
    import re

    lab = _lab()
    run_id = int(arguments["run_id"])
    text, origin = lab.log_for_run(run_id)
    lines = [line for line in (text or "").splitlines() if line.strip()]
    if arguments.get("grep"):
        try:
            regex = re.compile(arguments["grep"], re.IGNORECASE)
        except re.error as exc:
            raise TraceKitError(f"bad regex: {exc}") from exc
        lines = [line for line in lines if regex.search(line)]
    limit = int(arguments.get("max_lines") or 200)
    return {
        "run_id": run_id,
        "origin": origin,
        "lines": lines[:limit],
        "total_lines": len(lines),
        "truncated": len(lines) > limit,
    }


def tool_sweep_outcomes(arguments: dict) -> dict:
    batch_run_id = arguments.get("batch_run_id")
    return _lab().sweep_outcomes(int(batch_run_id) if batch_run_id else None)


DISPATCH = {
    "list_runs": tool_list_runs,
    "run_stats": tool_run_stats,
    "sync_traces": tool_sync_traces,
    "trace_digest": tool_trace_digest,
    "search_steps": tool_search_steps,
    "step_stats": tool_step_stats,
    "sweep_outcomes": tool_sweep_outcomes,
    "get_run_log": tool_get_run_log,
}


# ---------------------------------------------------------------- JSON-RPC plumbing


def _write(message: dict) -> None:
    sys.stdout.write(json.dumps(message, ensure_ascii=False, default=str) + "\n")
    sys.stdout.flush()


def _result(message_id, payload: dict) -> None:
    _write({"jsonrpc": "2.0", "id": message_id, "result": payload})


def _error(message_id, code: int, message: str) -> None:
    _write({"jsonrpc": "2.0", "id": message_id, "error": {"code": code, "message": message}})


def _handle(message: dict) -> None:
    method = message.get("method")
    message_id = message.get("id")
    params = message.get("params") or {}

    # A notification carries no id and must never be answered.
    if message_id is None and method is not None:
        return

    if method == "initialize":
        requested = params.get("protocolVersion")
        version = requested if requested in SUPPORTED_PROTOCOLS else LATEST_PROTOCOL
        _result(message_id, {
            "protocolVersion": version,
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": {"name": SERVER_NAME, "version": SERVER_VERSION},
            "instructions": INSTRUCTIONS,
        })
        return

    if method == "ping":
        _result(message_id, {})
        return

    if method == "tools/list":
        _result(message_id, {"tools": TOOLS})
        return

    if method == "tools/call":
        name = params.get("name")
        arguments = params.get("arguments") or {}
        if name not in DISPATCH:
            _error(message_id, -32602, f"unknown tool {name!r}; known: {', '.join(sorted(TOOL_NAMES))}")
            return
        try:
            payload = DISPATCH[name](arguments)
        except TraceKitError as exc:
            # A tool-level failure is reported inside the result so the model sees it and can adjust,
            # rather than as a protocol error it cannot act on.
            _result(message_id, {
                "content": [{"type": "text", "text": f"{name} failed: {exc}"}],
                "isError": True,
            })
            return
        except Exception as exc:  # noqa: BLE001 - a crash must not kill the server
            traceback.print_exc(file=sys.stderr)
            _result(message_id, {
                "content": [{"type": "text", "text": f"{name} raised {type(exc).__name__}: {exc}"}],
                "isError": True,
            })
            return
        _result(message_id, {
            "content": [{"type": "text", "text": json.dumps(payload, indent=2, default=str)}],
            "structuredContent": payload,
            "isError": False,
        })
        return

    if method in ("resources/list", "prompts/list"):
        # Advertised capabilities are tools only; answering these keeps a curious client happy.
        key = "resources" if method.startswith("resources") else "prompts"
        _result(message_id, {key: []})
        return

    _error(message_id, -32601, f"method not found: {method}")


def main() -> int:
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            message = json.loads(line)
        except json.JSONDecodeError:
            print(f"trace-mcp: ignoring non-JSON input: {line[:120]}", file=sys.stderr)
            continue
        try:
            _handle(message)
        except Exception as exc:  # noqa: BLE001 - keep serving
            traceback.print_exc(file=sys.stderr)
            if isinstance(message, dict) and message.get("id") is not None:
                _error(message["id"], -32603, f"internal error: {exc}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
