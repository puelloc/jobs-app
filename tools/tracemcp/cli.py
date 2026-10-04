#!/usr/bin/env python3
"""command-line front end for the trace analysis engine.

Deliberately dispatches through the same functions the MCP server exposes, so the CLI is a faithful
harness for the tools rather than a second implementation that can drift from them: if a subcommand
here is wrong, the corresponding MCP tool is wrong in the same way.

    python3 cli.py stats --platform career_listings
    python3 cli.py sync  --status error --max-runs 25
    python3 cli.py steps --platform career_listings
    python3 cli.py digest 615
    python3 cli.py search 'shadow DOM' --platform career_listings
    python3 cli.py log 615 --grep resolution
"""

from __future__ import annotations

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from server import DISPATCH  # noqa: E402


def _filters(args: argparse.Namespace) -> dict:
    """Translate the shared flag set into the tools' filter vocabulary."""
    mapping = {
        "platform": args.platform,
        "status": args.status,
        "company": args.company,
        "error_contains": args.error_contains,
        "min_duration_s": args.min_duration,
        "max_duration_s": args.max_duration,
        "since": args.since,
        "until": args.until,
        "min_id": args.min_id,
        "max_id": args.max_id,
        "has_trace": args.has_trace,
        "sort": args.sort,
        "limit": args.limit,
        "offset": args.offset,
    }
    return {key: value for key, value in mapping.items() if value is not None}


def _add_filters(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--platform")
    parser.add_argument("--status", choices=["ok", "error", "running"])
    parser.add_argument("--company")
    parser.add_argument("--error-contains", dest="error_contains")
    parser.add_argument("--min-duration", dest="min_duration", type=float)
    parser.add_argument("--max-duration", dest="max_duration", type=float)
    parser.add_argument("--since", help="ISO 8601")
    parser.add_argument("--until", help="ISO 8601")
    parser.add_argument("--min-id", dest="min_id", type=int)
    parser.add_argument("--max-id", dest="max_id", type=int)
    parser.add_argument("--has-trace", dest="has_trace", action="store_true", default=None)
    parser.add_argument("--sort", default="id_desc")
    parser.add_argument("--limit", type=int, default=50)
    parser.add_argument("--offset", type=int, default=0)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="tracekit", description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--source", choices=["local", "remote"], help="override TRACE_SOURCE")
    parser.add_argument("--cache", help="override TRACE_CACHE")
    sub = parser.add_subparsers(dest="command", required=True)

    runs = sub.add_parser("runs", help="list runs matching a filter")
    _add_filters(runs)

    stats = sub.add_parser("stats", help="aggregate statistics for matching runs")
    _add_filters(stats)
    stats.add_argument("--with-traces", action="store_true", default=False)

    sync = sub.add_parser("sync", help="fetch traces and logs into the local cache")
    _add_filters(sync)
    sync.add_argument("--max-runs", type=int, default=100)
    sync.add_argument("--force", action="store_true")

    steps = sub.add_parser("steps", help="step, action and timing statistics over cached traces")
    _add_filters(steps)

    digest = sub.add_parser("digest", help="one run's trace, condensed")
    digest.add_argument("run_id", type=int)
    digest.add_argument("--max-steps", type=int, default=40)
    digest.add_argument("--full-text", action="store_true")

    search = sub.add_parser("search", help="regex search across cached traces' step fields")
    search.add_argument("pattern")
    _add_filters(search)
    search.add_argument("--fields", nargs="*")

    log = sub.add_parser("log", help="a run's own log")
    log.add_argument("run_id", type=int)
    log.add_argument("--grep")
    log.add_argument("--max-lines", type=int, default=200)

    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)

    # The engine reads its configuration from the environment, so flags are applied by setting it.
    if args.source:
        os.environ["TRACE_SOURCE"] = args.source
    if args.cache:
        os.environ["TRACE_CACHE"] = args.cache

    arguments = _filters(args)
    tool = args.command
    if tool in ("stats",):
        arguments["with_traces"] = args.with_traces
    elif tool == "sync":
        arguments.update(max_runs=args.max_runs, force=args.force)
    elif tool == "digest":
        arguments.update(run_id=args.run_id, max_steps=args.max_steps, include_text=args.full_text)
    elif tool == "search":
        arguments.update(pattern=args.pattern)
        if args.fields:
            arguments["fields"] = args.fields
    elif tool == "log":
        arguments.update(run_id=args.run_id, grep=args.grep, max_lines=args.max_lines)

    name = {"runs": "list_runs", "stats": "run_stats", "steps": "step_stats", "sync": "sync_traces",
            "digest": "trace_digest", "search": "search_steps", "log": "get_run_log"}[tool]
    try:
        payload = DISPATCH[name](arguments)
    except Exception as exc:  # noqa: BLE001 - the CLI is a debugging surface, keep the message short
        print(f"tracekit {tool}: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 1

    # A digest is far easier to read with the step list last and unwrapped.
    print(json.dumps(payload, indent=2, default=str))
    return 0


if __name__ == "__main__":
    sys.exit(main())
