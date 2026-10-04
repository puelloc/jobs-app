#!/usr/bin/env python3
"""logs MCP server: query the central log store (Loki), over stdio.

The sibling of tools/tracemcp. That one reads jobs-app's structured run and trace data directly; this
one reads the logs every project ships to Loki, which is what makes a failure that crosses processes
(server triggers a sweep, batch runs a company, the worker fails) into one readable timeline.

Speaks JSON-RPC 2.0 on stdin/stdout (newline-delimited), the MCP stdio transport. Stdlib only: it runs
with whatever ``python3`` is on PATH.
"""

from __future__ import annotations

import json
import os
import sys
import traceback

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from logkit import LogKit, LogKitError, Loki, parse_window  # noqa: E402

SERVER_NAME = "logs"
SERVER_VERSION = "0.1.0"

# The revisions the DSH MCP client negotiates; the newest is advertised and whatever the client asks
# for is echoed back when it is one of these, so a version skew cannot break the handshake.
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
    "Reads the central log store (Loki) that every project ships to: jobs-app, apply-app and "
    "apply-console. Start with status to see which labels exist, then error_summary to find what is "
    "failing. When you have one trace_id - from an error line, a run, or the traces MCP server - "
    "trace_timeline replays that entire unit of work across every service in order, which is the "
    "fastest route to a root cause. search_logs takes raw LogQL for anything else. "
    "Apps log one JSON object per line on stderr (fields: level, app, svc, msg, trace_id, span, "
    "sweep_id, run_id, company); their human-readable reports go to stdout and are searchable too, "
    "just unparsed."
)

# ---------------------------------------------------------------- tool schemas

WINDOW = {
    "type": "string",
    "description": "Relative window: a duration like 90m, 2h, 7d, or an ISO 8601 instant (meaning 'since then').",
    "default": "24h",
}

TOOLS: list[dict] = [
    {
        "name": "status",
        "description": (
            "What the log store currently holds: the label names present and the values of app, svc "
            "and level. Call this first when you do not know what is being collected."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {"window": WINDOW},
            "additionalProperties": False,
        },
    },
    {
        "name": "error_summary",
        "description": (
            "Grouped failure counts over a window, by app, service and message. Counting is done by "
            "the log store, so the totals are exact rather than limited by how many lines were "
            "fetched. Use this to answer 'what is broken right now'."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "window": WINDOW,
                "level": {
                    "type": "string",
                    "description": "Log level regex to count. Defaults to 'error'; 'error|warn' widens it.",
                    "default": "error",
                },
                "app": {"type": "string", "description": "Restrict to one app, e.g. jobs-app."},
                "limit": {"type": "integer", "default": 30},
            },
            "additionalProperties": False,
        },
    },
    {
        "name": "trace_timeline",
        "description": (
            "Everything that happened during one unit of work, in order, across every service. Give "
            "it a trace_id (32 hex characters) and it replays the request, the job it triggered, and "
            "everything that job did. This is the tool for resolving a failure end to end."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "trace_id": {"type": "string", "description": "32 lowercase hex characters."},
                "window": {"type": "string", "default": "7d"},
                "limit": {"type": "integer", "default": 500},
            },
            "required": ["trace_id"],
            "additionalProperties": False,
        },
    },
    {
        "name": "search_logs",
        "description": (
            "Raw LogQL query, for anything the other tools do not anticipate. Examples: "
            "'{app=\"jobs-app\"} | json | company=\"abbott-laboratories\"', "
            "'{svc=\"batch\"} | json | duration_ms > 600000'. Filters on parsed fields use `| field = "
            "value`; line filters use `|= \"text\"`."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {
                "query": {"type": "string"},
                "window": {"type": "string", "default": "1h"},
                "limit": {"type": "integer", "default": 200},
                "direction": {
                    "type": "string",
                    "enum": ["backward", "forward"],
                    "default": "backward",
                    "description": "backward = newest first (default); forward = oldest first.",
                },
            },
            "required": ["query"],
            "additionalProperties": False,
        },
    },
    {
        "name": "container_health",
        "description": (
            "Per-container error density over a window: how many lines each container produced and "
            "how many look like failures, ranked by density. Finds the container that is actually "
            "misbehaving rather than the one that is merely noisy."
        ),
        "inputSchema": {
            "type": "object",
            "properties": {"window": WINDOW, "limit": {"type": "integer", "default": 40}},
            "additionalProperties": False,
        },
    },
]

TOOL_NAMES = {tool["name"] for tool in TOOLS}


# ---------------------------------------------------------------- implementations


def _kit() -> LogKit:
    return LogKit(Loki())


def tool_status(arguments: dict) -> dict:
    return _kit().status(arguments.get("window") or "24h")


def tool_error_summary(arguments: dict) -> dict:
    return _kit().error_summary(
        window=arguments.get("window") or "24h",
        level=arguments.get("level") or "error",
        app=arguments.get("app") or "",
        limit=int(arguments.get("limit") or 30),
    )


def tool_trace_timeline(arguments: dict) -> dict:
    return _kit().trace_timeline(
        arguments["trace_id"],
        window=arguments.get("window") or "7d",
        limit=int(arguments.get("limit") or 500),
    )


def tool_search_logs(arguments: dict) -> dict:
    return _kit().search_logs(
        arguments["query"],
        window=arguments.get("window") or "1h",
        limit=int(arguments.get("limit") or 200),
        direction=arguments.get("direction") or "backward",
    )


def tool_container_health(arguments: dict) -> dict:
    return _kit().container_health(
        window=arguments.get("window") or "24h",
        limit=int(arguments.get("limit") or 40),
    )


DISPATCH = {
    "status": tool_status,
    "error_summary": tool_error_summary,
    "trace_timeline": tool_trace_timeline,
    "search_logs": tool_search_logs,
    "container_health": tool_container_health,
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
        except LogKitError as exc:
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
            print(f"logs-mcp: ignoring non-JSON input: {line[:120]}", file=sys.stderr)
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
