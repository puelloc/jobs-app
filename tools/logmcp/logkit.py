"""logkit: the query engine behind the logs MCP server.

Turns Loki's LogQL API into the handful of questions worth asking about a pipeline that logs from
three projects: what is broken, what happened during this one unit of work, and which container is
misbehaving. It is the log counterpart to tools/tracemcp, which reads the structured run and trace data
directly out of jobs-app.

Stdlib only, like its sibling: this runs with whatever ``python3`` is on PATH, and an analysis tool is
the last thing that should break because a language runtime moved.
"""

from __future__ import annotations

import json
import os
import re
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone

DEFAULT_LOKI = "http://127.0.0.1:3100"

# Log lines that mark a container as unhealthy. Deliberately a small, boring list: a pattern that
# matches everything reports nothing.
CONTAINER_PROBLEM_PATTERN = "(?i)panic|fatal|oomkill|out of memory|traceback|exit status|unhandled"


# Identifier fields, where 0 means "never set" rather than a value. See parsed_line.
ID_FIELDS = {"run_id", "sweep_id", "listing_id", "application_id", "console_run_id"}


class LogKitError(RuntimeError):
    """A failure worth showing the caller verbatim."""


def parse_window(value: str | None, default: str = "24h") -> timedelta:
    """Read a window as a duration ("90m", "2h", "7d") or as an ISO instant (then: how long ago).

    A window rather than an absolute range, because every question worth asking is relative - "the last
    sweep", "since it broke" - and relative windows keep working when the caller has no idea what time
    it is in the server's zone.
    """
    text = (value or default).strip()
    match = re.fullmatch(r"(\d+)\s*([smhdw])", text)
    if match:
        amount = int(match.group(1))
        unit = match.group(2)
        return timedelta(seconds=amount * {"s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800}[unit])
    instant = _parse_instant(text)
    if instant is not None:
        delta = datetime.now(timezone.utc) - instant
        return delta if delta > timedelta(0) else timedelta(seconds=1)
    raise LogKitError(f"bad window {value!r}: use a duration like 90m, 2h, 7d, or an ISO 8601 instant")


def _parse_instant(text: str) -> datetime | None:
    try:
        parsed = datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return None
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)


def _now_ns() -> int:
    return int(datetime.now(timezone.utc).timestamp() * 1_000_000_000)


def _iso(ns: int) -> str:
    return datetime.fromtimestamp(ns / 1_000_000_000, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


class Loki:
    """A Loki client that can be pointed at a fake in tests by overriding `_get`."""

    def __init__(self, url: str | None = None, timeout: float = 30.0) -> None:
        self.url = (url or os.environ.get("LOKI_URL") or DEFAULT_LOKI).rstrip("/")
        self.timeout = float(os.environ.get("LOKI_TIMEOUT", timeout))

    def _get(self, path: str, params: dict) -> dict:
        url = f"{self.url}{path}?{urllib.parse.urlencode(params)}"
        try:
            with urllib.request.urlopen(url, timeout=self.timeout) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            detail = ""
            try:
                detail = exc.read().decode("utf-8", "replace")[:300]
            except Exception:  # noqa: BLE001
                pass
            raise LogKitError(f"{path} -> HTTP {exc.code} {detail}") from exc
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise LogKitError(f"{path} -> {exc} (is Loki reachable at {self.url}?)") from exc

    # ------------------------------------------------------------------ primitives

    def query_range(self, query: str, window: timedelta, limit: int = 200, direction: str = "backward") -> dict:
        end = _now_ns()
        start = end - int(window.total_seconds() * 1_000_000_000)
        return self._get("/loki/api/v1/query_range", {
            "query": query,
            "start": start,
            "end": end,
            "limit": limit,
            "direction": direction,
        })

    def query_instant(self, query: str, at_ns: int | None = None) -> dict:
        return self._get("/loki/api/v1/query", {"query": query, "time": at_ns or _now_ns()})

    def labels(self, window: timedelta) -> list[str]:
        end = _now_ns()
        start = end - int(window.total_seconds() * 1_000_000_000)
        body = self._get("/loki/api/v1/labels", {"start": start, "end": end})
        return sorted(body.get("data") or [])

    def label_values(self, name: str, window: timedelta) -> list[str]:
        end = _now_ns()
        start = end - int(window.total_seconds() * 1_000_000_000)
        body = self._get(f"/loki/api/v1/label/{urllib.parse.quote(name)}/values", {"start": start, "end": end})
        return sorted(body.get("data") or [])


def streams_to_lines(body: dict, limit: int = 200) -> list[dict]:
    """Flatten a Loki result into time-ordered lines with their stream labels."""
    lines: list[dict] = []
    for stream in (body.get("data") or {}).get("result") or []:
        labels = stream.get("stream") or {}
        for value in stream.get("values") or []:
            if not isinstance(value, list) or len(value) < 2:
                continue
            try:
                timestamp = int(value[0])
            except (TypeError, ValueError):
                continue
            lines.append({"ts_ns": timestamp, "labels": labels, "line": value[1]})
    lines.sort(key=lambda entry: entry["ts_ns"])
    return lines[:limit]


def parsed_line(entry: dict) -> dict:
    """One log line as a flat record: its labels, its parsed JSON body when it is JSON, and its raw
    text when it is not. Non-JSON lines are kept rather than dropped - a human report or a Python
    traceback is exactly the sort of thing worth seeing next to the structured lines."""
    out = {
        "ts": _iso(entry["ts_ns"]),
        "container": entry["labels"].get("container") or entry["labels"].get("service") or "",
    }
    text = entry["line"]
    try:
        body = json.loads(text)
    except (json.JSONDecodeError, TypeError):
        out["raw"] = text[:2000]
        return out
    if not isinstance(body, dict):
        out["raw"] = text[:2000]
        return out
    for key in ("level", "app", "svc", "span", "msg", "trace_id", "run_id", "sweep_id", "company",
                "error", "status", "path", "method", "duration_ms"):
        if key not in body or body[key] in (None, ""):
            continue
        # An id of 0 is not an id: run and sweep ids start at 1, so a zero means the field was never
        # set, and reporting it would invent a run that does not exist. Zero stays meaningful for
        # anything measurable - duration_ms of 0 is a fast request, not a missing one - so this is
        # limited to identifiers.
        if key in ID_FIELDS and body[key] in (0, "0"):
            continue
        out[key] = body[key]
    # Anything else the app logged is preserved, so a field added later is visible without a change
    # here - the whole point of asking apps to log structured JSON.
    extra = {
        key: value for key, value in body.items()
        if key not in out and key not in ("ts",) and value not in (None, "", [], {})
    }
    if extra:
        out["fields"] = extra
    return out


class LogKit:
    """The queries behind the MCP tools."""

    def __init__(self, loki: Loki | None = None) -> None:
        self.loki = loki or Loki()

    # ------------------------------------------------------------------ tools

    def status(self, window: str = "24h") -> dict:
        """What is in the store and which labels exist, so a caller can orient before querying."""
        span = parse_window(window)
        labels = self.loki.labels(span)
        values = {}
        for name in ("app", "svc", "level"):
            if name in labels:
                try:
                    values[name] = self.loki.label_values(name, span)
                except LogKitError:
                    values[name] = []
        return {"loki_url": self.loki.url, "window": str(span), "labels": labels, "label_values": values}

    def search_logs(
        self,
        query: str,
        window: str = "1h",
        limit: int = 200,
        direction: str = "backward",
    ) -> dict:
        """Raw LogQL, for questions the other tools do not anticipate."""
        span = parse_window(window)
        body = self.loki.query_range(query, span, limit=limit, direction=direction)
        lines = streams_to_lines(body, limit=limit)
        return {
            "query": query,
            "window": str(span),
            "returned": len(lines),
            "lines": [parsed_line(entry) for entry in lines],
        }

    def trace_timeline(self, trace_id: str, window: str = "7d", limit: int = 500) -> dict:
        """Everything that happened during one unit of work, across every app, in order.

        This is the tool that answers "what went wrong here": a sweep's trace covers the request that
        triggered it, the batch that ran it, and every company it visited - three processes, three
        services, one trace id.
        """
        trace_id = trace_id.strip().lower()
        if not re.fullmatch(r"[0-9a-f]{32}", trace_id):
            raise LogKitError(f"trace_id must be 32 lowercase hex characters, got {trace_id!r}")
        span = parse_window(window)
        # trace_id is structured metadata, so it is filtered after parsing rather than being a label.
        query = f'{{app=~".+"}} | json | trace_id="{trace_id}"'
        body = self.loki.query_range(query, span, limit=limit, direction="forward")
        lines = [parsed_line(entry) for entry in streams_to_lines(body, limit=limit)]

        spans: dict[str, int] = {}
        for line in lines:
            spans[str(line.get("span") or "-")] = spans.get(str(line.get("span") or "-"), 0) + 1
        errors = [line for line in lines if str(line.get("level")) in ("error", "warn", "fatal")]
        return {
            "trace_id": trace_id,
            "window": str(span),
            "lines": len(lines),
            "spans": spans,
            "errors": len(errors),
            "timeline": lines,
            "first": lines[0]["ts"] if lines else None,
            "last": lines[-1]["ts"] if lines else None,
        }

    def error_summary(
        self,
        window: str = "24h",
        level: str = "error",
        limit: int = 30,
        app: str = "",
    ) -> dict:
        """Which failures are happening, how many, and when each was first and last seen.

        Counted by Loki rather than by fetching lines: `count_over_time` over a parsed and grouped
        stream gives the true total without pulling every line through this process.
        """
        span = parse_window(window)
        seconds = max(1, int(span.total_seconds()))
        selector = f'{{level=~"{level}"}}' if not app else f'{{level=~"{level}",app="{app}"}}'
        query = f'sum by (app, svc, msg) (count_over_time({selector} | json [{seconds}s]))'
        body = self.loki.query_instant(query)
        rows = []
        for result in (body.get("data") or {}).get("result") or []:
            metric = result.get("metric") or {}
            value = result.get("value") or [None, "0"]
            try:
                count = float(value[1])
            except (TypeError, ValueError, IndexError):
                continue
            rows.append({
                "app": metric.get("app") or "",
                "svc": metric.get("svc") or "",
                "msg": metric.get("msg") or "(unparsed)",
                "count": int(count),
            })
        rows.sort(key=lambda row: -row["count"])
        return {
            "window": str(span),
            "level": level,
            "groups": len(rows),
            "total": sum(row["count"] for row in rows),
            "errors": rows[:limit],
            "query": query,
        }

    def container_health(self, window: str = "24h", limit: int = 40) -> dict:
        """Per-container error density: which containers are producing failures, and how many lines
        they produce at all.

        Note what this is not: it reads log lines, so it sees a container *saying* it crashed and not
        a container being killed before it could. Restart counts need the Docker events API, which is
        deliberately out of scope for a log reader - the startup banner (`server starting`) is the
        honest in-log proxy, and it is counted when present.
        """
        span = parse_window(window)
        seconds = max(1, int(span.total_seconds()))
        total = self.loki.query_instant(
            f'sum by (container) (count_over_time({{container=~".+"}} [{seconds}s]))'
        )
        problems = self.loki.query_instant(
            f'sum by (container) (count_over_time({{container=~".+"}} |~ "{CONTAINER_PROBLEM_PATTERN}" [{seconds}s]))'
        )

        def rows(body: dict) -> dict[str, int]:
            out: dict[str, int] = {}
            for result in (body.get("data") or {}).get("result") or []:
                name = (result.get("metric") or {}).get("container") or ""
                try:
                    out[name] = int(float((result.get("value") or [None, "0"])[1]))
                except (TypeError, ValueError, IndexError):
                    continue
            return out

        totals = rows(total)
        bad = rows(problems)
        containers = []
        for name, count in sorted(totals.items(), key=lambda kv: -kv[1])[:limit]:
            flagged = bad.get(name, 0)
            containers.append({
                "container": name,
                "lines": count,
                "problem_lines": flagged,
                "problem_per_1k": round(flagged / count * 1000, 1) if count else 0.0,
            })
        containers.sort(key=lambda row: (-row["problem_per_1k"], -row["problem_lines"]))
        return {
            "window": str(span),
            "problem_pattern": CONTAINER_PROBLEM_PATTERN,
            "containers": containers,
            "note": "reads log lines, so it sees a container reporting a problem, not one killed silently",
        }
