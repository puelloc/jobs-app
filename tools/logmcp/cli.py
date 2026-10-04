#!/usr/bin/env python3
"""command-line front end for the log query engine.

Dispatches through the same functions the MCP server exposes, so the CLI is a faithful harness for the
tools rather than a second implementation that can drift from them.

    python3 cli.py status
    python3 cli.py errors --window 2h
    python3 cli.py timeline 9f2c... --window 7d
    python3 cli.py search '{app="jobs-app"} | json | company="cisco"' --window 24h
    python3 cli.py health --window 6h
"""

from __future__ import annotations

import argparse
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from server import DISPATCH  # noqa: E402


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="logkit", description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--loki", help="Loki base URL (default LOKI_URL or http://127.0.0.1:3100)")
    sub = parser.add_subparsers(dest="command", required=True)

    status = sub.add_parser("status", help="labels and values the store currently holds")
    status.add_argument("--window", default="24h")

    errors = sub.add_parser("errors", help="grouped failure counts")
    errors.add_argument("--window", default="24h")
    errors.add_argument("--level", default="error")
    errors.add_argument("--app", default="")
    errors.add_argument("--limit", type=int, default=30)

    timeline = sub.add_parser("timeline", help="replay one trace across every service")
    timeline.add_argument("trace_id")
    timeline.add_argument("--window", default="7d")
    timeline.add_argument("--limit", type=int, default=500)

    search = sub.add_parser("search", help="raw LogQL")
    search.add_argument("query")
    search.add_argument("--window", default="1h")
    search.add_argument("--limit", type=int, default=200)
    search.add_argument("--direction", choices=["backward", "forward"], default="backward")

    health = sub.add_parser("health", help="per-container error density")
    health.add_argument("--window", default="24h")
    health.add_argument("--limit", type=int, default=40)

    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.loki:
        os.environ["LOKI_URL"] = args.loki

    if args.command == "status":
        payload = DISPATCH["status"]({"window": args.window})
    elif args.command == "errors":
        payload = DISPATCH["error_summary"]({
            "window": args.window, "level": args.level, "app": args.app, "limit": args.limit,
        })
    elif args.command == "timeline":
        payload = DISPATCH["trace_timeline"]({
            "trace_id": args.trace_id, "window": args.window, "limit": args.limit,
        })
    elif args.command == "search":
        payload = DISPATCH["search_logs"]({
            "query": args.query, "window": args.window,
            "limit": args.limit, "direction": args.direction,
        })
    else:
        payload = DISPATCH["container_health"]({"window": args.window, "limit": args.limit})

    print(json.dumps(payload, indent=2, default=str))
    return 0


if __name__ == "__main__":
    sys.exit(main())
