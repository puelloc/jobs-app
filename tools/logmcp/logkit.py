"""logkit: the query engine behind the logs MCP server.

Turns Loki's LogQL API into the handful of questions worth asking about a pipeline that logs from
three projects: what is broken, what happened during this one unit of work, and which container is
misbehaving. It is the log counterpart to tools/tracemcp, which reads the structured run and trace data
directly out of jobs-app.

Stdlib only, like its sibling: this runs with whatever ``python3`` is on PATH, and an analysis tool is
the last thing that should break because a language runtime moved.
"""

from __future__ import annotations

import base64
import json
import os
import re
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone
from typing import Any

DEFAULT_LOKI = "http://127.0.0.1:3100"

# Log lines that mark a container as unhealthy. Deliberately a small, boring list: a pattern that
# matches everything reports nothing.
CONTAINER_PROBLEM_PATTERN = "(?i)panic|fatal|oomkill|out of memory|traceback|exit status|unhandled"


# Labels worth counting, and the only names allowed into a generated query.
COUNTED_LABELS = ("level", "app", "svc")

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

    def __init__(
        self,
        url: str | None = None,
        timeout: float = 30.0,
        username: str | None = None,
        password: str | None = None,
        token: str | None = None,
    ) -> None:
        self.url = (url or os.environ.get("LOKI_URL") or DEFAULT_LOKI).rstrip("/")
        self.timeout = float(os.environ.get("LOKI_TIMEOUT", timeout))
        # Loki has no authentication of its own, so whatever exposes it supplies one. The two common
        # shapes are an Nginx Proxy Manager Access List (HTTP Basic) and a reverse proxy expecting a
        # bearer token; both are supported, because a query tool that cannot authenticate against the
        # store it is pointed at is no use.
        self.username = username or os.environ.get("LOKI_USERNAME") or ""
        self.password = password or os.environ.get("LOKI_PASSWORD") or ""
        self.token = token or os.environ.get("LOKI_TOKEN") or ""

    def headers(self) -> dict:
        """Every request's headers, including credentials when they are configured."""
        headers = {"Accept": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        elif self.username:
            raw = f"{self.username}:{self.password}".encode()
            headers["Authorization"] = "Basic " + base64.b64encode(raw).decode()
        return headers

    def push(self, streams: list[dict]) -> int:
        """Write log lines. Used by the tests to prove the authenticated path works end to end."""
        request = urllib.request.Request(
            f"{self.url}/loki/api/v1/push",
            data=json.dumps({"streams": streams}).encode(),
            headers={**self.headers(), "Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                return response.status
        except urllib.error.HTTPError as exc:
            raise LogKitError(f"/loki/api/v1/push -> HTTP {exc.code}") from exc
        except (urllib.error.URLError, TimeoutError) as exc:
            raise LogKitError(f"/loki/api/v1/push -> {exc}") from exc

    def _get(self, path: str, params: dict) -> dict:
        url = f"{self.url}{path}?{urllib.parse.urlencode(params)}"
        request = urllib.request.Request(url, headers=self.headers())
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
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


# ---------------------------------------------------------------------------- lookups

# The keys a lookup can be built on. They are structured metadata in the store rather than labels, which
# is why they can be filtered without parsing the line: see LogKit._metadata_lines.
LOOKUP_KEYS = ("trace_id", "run_id", "sweep_id", "company", "listing_id", "application_id")


def _logql_string(value: str) -> str:
    """A LogQL string literal, escaped.

    An unescaped quote does not fail loudly here: the query errors, the caller sees no lines, and "no
    such company" and "I broke the query" become the same answer.
    """
    escaped = value.replace("\\", "\\\\").replace('"', '\\"').replace("\n", " ").replace("\r", " ")
    return f'"{escaped}"'


def _field(line: dict, key: str):
    """A field from a parsed line, wherever it landed.

    parsed_line promotes a fixed set of well-known fields to the top level and leaves everything else
    under `fields`, so a field an app starts logging after this file was written is still readable here.
    """
    if key in line:
        return line[key]
    fields = line.get("fields")
    if isinstance(fields, dict):
        return fields.get(key)
    return None


def _clean(value):
    """Normalise "absent" so a summary reports what is known instead of what defaulted to zero."""
    return None if value in (None, "") else value


def _by_msg(lines: list[dict], msg: str) -> dict:
    for line in lines:
        if line.get("msg") == msg:
            return line
    return {}


def _all_msg(lines: list[dict], msg: str) -> list[dict]:
    return [line for line in lines if line.get("msg") == msg]


def _run_facts(lines: list[dict]) -> dict:
    """How one unit of work ended and what it cost, from the lines that belong to it.

    Shared by the run and sweep lookups so a company's outcome is derived the same way in both, rather
    than one summary disagreeing with the other.
    """
    summary = _by_msg(lines, "summary")
    skip = _by_msg(lines, "skip")
    decided = _by_msg(lines, "agent decided")
    agent_failed = _by_msg(lines, "agent failed")
    blocked = _by_msg(lines, "listings page blocked")
    finished = _by_msg(lines, "company finished")
    stored = _all_msg(lines, "listing stored")
    steps = _all_msg(lines, "agent step")

    # Most specific evidence first: a run that failed to reach an answer is not "completed" merely
    # because something later wrote a summary line.
    if _by_msg(lines, "company failed"):
        # The batch could not launch the scrape at all - an exec failure, a missing interpreter - which
        # is a different problem from a scrape that ran and failed.
        outcome = "failed_to_run"
    elif agent_failed:
        outcome = "agent_failed"
    elif blocked or _field(summary, "blocked") is True:
        outcome = "blocked"
    elif skip:
        # `reason` is an application field rather than one of the promoted ones, so it lives under
        # `fields`. Reading it from the top level silently degraded every skip to the word "skipped",
        # which loses the distinction that matters: no_remote_roles is a finding, robots_disallowed is
        # a company we never actually looked at.
        outcome = str(_field(skip, "reason") or "skipped")
    elif stored:
        outcome = "stored"
    elif summary:
        outcome = "completed"
    else:
        outcome = "in_progress"

    reported = _field(decided, "agent_steps")
    if not isinstance(reported, int):
        reported = _field(agent_failed, "agent_steps")
    agent_steps = len(steps) or (reported if isinstance(reported, int) else None)

    return {
        "outcome": outcome,
        "agent_steps": agent_steps,
        "agent_s": _clean(_field(summary, "agent_s") or _field(decided, "agent_s")),
        "total_s": _clean(_field(summary, "total_s")),
        "duration_ms": _clean(_field(finished, "duration_ms")),
        "quality": _clean(_field(summary, "quality")),
        "block_reason": _clean(_field(blocked, "block_reason") or _field(summary, "block_reason")),
        "listings_stored": len(stored),
        "listing_ids": [i for i in (_field(s, "listing_id") for s in stored) if i],
    }


def _summarise_run(run_id: int, lines: list[dict], span: timedelta) -> dict:
    if not lines:
        return {
            "run_id": run_id,
            "window": str(span),
            "found": False,
            "lines": 0,
            "note": ("nothing for this run in the window - it may be older than the window, or older "
                     "than the structured logging itself"),
        }
    start = _by_msg(lines, "start")
    decided = _by_msg(lines, "agent decided")
    fetched = _by_msg(lines, "fetch")
    stale = _by_msg(lines, "stale")
    facts = _run_facts(lines)
    return {
        "run_id": run_id,
        "window": str(span),
        "found": True,
        "lines": len(lines),
        "company": _clean(start.get("company") or _by_msg(lines, "summary").get("company")),
        "sweep_id": _clean(start.get("sweep_id")),
        "vendor": _clean(_field(start, "vendor")),
        "listings_url": _clean(_field(decided, "listings_url") or _field(start, "listings_url")),
        **facts,
        "agent": {
            "remote_confirmed": _field(decided, "remote_confirmed"),
            "judged": _field(decided, "agent_judged"),
            "judgement": _clean(_field(decided, "agent_judgement")),
            "role_count": _clean(_field(decided, "role_count")),
            "failed": _clean(_field(_by_msg(lines, "agent failed"), "error")),
            "blocked": bool(_by_msg(lines, "listings page blocked")),
        },
        "postings": {
            "extracted": _clean(_field(fetched, "extracted")),
            "inserted": _clean(_field(_by_msg(lines, "summary"), "inserted")),
            "refreshed": _clean(_field(_by_msg(lines, "summary"), "refreshed")),
            "skipped_non_us": _clean(_field(_by_msg(lines, "summary"), "skipped_non_us")),
            "closed": _clean(_field(stale, "closed")),
        },
        "step_urls": [_field(s, "url") for s in _all_msg(lines, "agent step")],
        "errors": len([l for l in lines if str(l.get("level")) in ("error", "fatal")]),
        "warnings": len([l for l in lines if str(l.get("level")) == "warn"]),
        "timeline": lines,
    }


def _summarise_sweep(sweep_id: int, lines: list[dict], span: timedelta) -> dict:
    if not lines:
        return {"sweep_id": sweep_id, "window": str(span), "found": False, "lines": 0}
    started = _by_msg(lines, "sweep started")
    finished = _by_msg(lines, "sweep finished")
    skipped = _all_msg(lines, "company skipped")

    # Group by run_id, then re-attach the company's own batch lines.
    #
    # Grouping by `company` when a line had no run_id made phantom companies: `company started` is a
    # batch-level line, so each one became its own "in_progress" row alongside the run it announced.
    # The batch lines still belong to the company's story - `company finished` carries the duration and
    # `company failed` means the scrape never launched - so they are attached rather than discarded.
    by_run: dict[Any, list[dict]] = {}
    for line in lines:
        run_id = line.get("run_id")
        if run_id is not None:
            by_run.setdefault(run_id, []).append(line)

    company_lines: dict[str, list[dict]] = {}
    for line in lines:
        if line.get("run_id") is None and line.get("msg") in (
                "company started", "company finished", "company failed"):
            company = line.get("company")
            if company:
                company_lines.setdefault(company, []).append(line)

    groups: list[tuple[Any, list[dict]]] = []
    attached: set[str] = set()
    for run_id, group in by_run.items():
        company = next((l.get("company") for l in group if l.get("company")), None)
        extra = company_lines.get(company, []) if company else []
        if company:
            attached.add(company)
        groups.append((run_id, group + extra))
    # A company the batch started whose scrape produced no run at all: it failed before logging a start.
    for company, group in company_lines.items():
        if company not in attached:
            groups.append((None, group))

    rows = []
    for run_id, group in groups:
        facts = _run_facts(group)
        start = _by_msg(group, "start")
        rows.append({
            "run_id": run_id,
            "company": _clean(start.get("company") or group[0].get("company")),
            "vendor": _clean(_field(start, "vendor")),
            **{k: facts[k] for k in ("outcome", "agent_steps", "agent_s", "duration_ms", "quality",
                                     "block_reason", "listings_stored")},
        })
    rows.sort(key=lambda row: (row["run_id"] is None, row["run_id"] or 0))

    plan = {
        "companies": _clean(_field(started, "companies")),
        "candidates": _clean(_field(started, "candidates")),
        "limit": _clean(_field(started, "limit")),
        "skip_ok": _field(started, "skip_ok"),
        "skip_traced": _field(started, "skip_traced"),
        "from_slug": _clean(_field(started, "from_slug")),
    }
    outcomes: dict[str, int] = {}
    for row in rows:
        outcomes[row["outcome"]] = outcomes.get(row["outcome"], 0) + 1
    return {
        "sweep_id": sweep_id,
        "window": str(span),
        "found": True,
        "lines": len(lines),
        "plan": plan,
        "skipped_before_starting": [
            {"company": s.get("company"), "reason": _field(s, "reason")} for s in skipped
        ],
        "companies": rows,
        "outcomes": outcomes,
        "finished": {
            "ok": _clean(_field(finished, "ok")),
            "failed": _clean(_field(finished, "failed")),
            "skipped": _clean(_field(finished, "skipped")),
            "stopped_early": _field(finished, "stopped_early"),
        } if finished else None,
        "errors": len([l for l in lines if str(l.get("level")) in ("error", "fatal")]),
    }


def _summarise_company(company: str, lines: list[dict], span: timedelta) -> dict:
    if not lines:
        return {"company": company, "window": str(span), "found": False, "lines": 0,
                "note": "no lines for this company in the window"}
    by_run: dict[Any, list[dict]] = {}
    for line in lines:
        key = line.get("run_id")
        if key is None:
            continue
        by_run.setdefault(key, []).append(line)

    rows = []
    for run_id, group in by_run.items():
        facts = _run_facts(group)
        start = _by_msg(group, "start")
        rows.append({
            "run_id": run_id,
            "sweep_id": _clean(start.get("sweep_id")),
            "at": group[0].get("ts"),
            "vendor": _clean(_field(start, "vendor")),
            "reason": None,
            **{k: facts[k] for k in ("outcome", "agent_steps", "agent_s", "total_s", "quality",
                                     "block_reason")},
        })

    # A company the batch skipped never ran, so it has no run_id - and dropping those lines made this
    # report zero history for a company the sweep considered and passed over, which is the opposite of
    # the question it exists to answer. "Skipped on 14 consecutive sweeps because the last run was ok"
    # and "never seen" are very different answers.
    for line in _all_msg(lines, "company skipped"):
        rows.append({
            "run_id": None,
            "sweep_id": _clean(line.get("sweep_id")),
            "at": line.get("ts"),
            "vendor": None,
            "outcome": "skipped_before_running",
            "reason": _clean(_field(line, "reason")),
            "agent_steps": None,
            "agent_s": None,
            "total_s": None,
            "quality": None,
            "block_reason": None,
        })

    rows.sort(key=lambda row: (row["at"] or "", row["run_id"] or 0), reverse=True)
    outcomes: dict[str, int] = {}
    for row in rows:
        outcomes[row["outcome"]] = outcomes.get(row["outcome"], 0) + 1
    return {
        "company": company,
        "window": str(span),
        "found": True,
        "lines": len(lines),
        "runs": len([r for r in rows if r["run_id"] is not None]),
        "skipped": len([r for r in rows if r["run_id"] is None]),
        "outcomes": outcomes,
        "history": rows,
        "errors": len([l for l in lines if str(l.get("level")) in ("error", "fatal")]),
    }


def _summarise_listing(listing_id: int, lines: list[dict], span: timedelta) -> dict:
    if not lines:
        return {"listing_id": listing_id, "window": str(span), "found": False, "lines": 0,
                "note": ("nothing references this listing yet - jobs-app logs 'listing stored' when it "
                         "saves one, and apply-app must log listing_id for its side to appear")}
    stored = _all_msg(lines, "listing stored")
    # Keyed on the event rather than on an app field: the field can be absent on a line that is still
    # relevant (a non-JSON line, or an app that has not adopted it yet), and then a real application
    # would be filed as a discovery.
    applications = [l for l in lines if l.get("msg") != "listing stored"]
    first = stored[0] if stored else lines[0]
    return {
        "listing_id": listing_id,
        "window": str(span),
        "found": True,
        "lines": len(lines),
        "company": _clean(first.get("company")),
        "title": _clean(_field(first, "title")),
        "url": _clean(_field(first, "url")),
        "is_remote": _field(first, "is_remote"),
        "discovered": [
            {"at": s.get("ts"), "run_id": s.get("run_id"), "company": s.get("company"),
             "is_new": _field(s, "is_new")}
            for s in stored
        ],
        "applications": [
            {"at": a.get("ts"), "app": a.get("app"), "msg": a.get("msg"), "run_id": a.get("run_id")}
            for a in applications
        ],
        "timeline": lines,
    }


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

    def _value_counts(self, name: str, span: timedelta) -> dict[str, int]:
        """How many lines each value of a label actually has in the window."""
        if name not in COUNTED_LABELS:
            # The name is interpolated into a query, so it may only ever come from this list.
            raise LogKitError(f"refusing to count unknown label {name!r}")
        seconds = max(1, int(span.total_seconds()))
        body = self.loki.query_instant(
            f'sum by ({name}) (count_over_time({{{name}=~".+"}} [{seconds}s]))'
        )
        counts: dict[str, int] = {}
        for result in (body.get("data") or {}).get("result") or []:
            metric = result.get("metric") or {}
            value = result.get("value") or [None, "0"]
            try:
                counts[metric.get(name) or ""] = int(float(value[1]))
            except (TypeError, ValueError, IndexError):
                continue
        return counts

    def status(self, window: str = "24h") -> dict:
        """What the store holds, which labels exist, and what is actually arriving.

        The value lists matter less than they look. Loki's label API reports values from the *index*,
        not from the window, so against a real store it happily lists `level=error` when nothing has
        logged an error for days - and an agent reading that as "there are errors" would chase a ghost.
        Pairing each value with its count here also distinguishes "the system is quiet" from "the
        collector is broken", which look identical from a list of values.
        """
        span = parse_window(window)
        labels = self.loki.labels(span)
        values: dict[str, list] = {}
        counts: dict[str, dict] = {}
        for name in COUNTED_LABELS:
            if name not in labels:
                continue
            try:
                values[name] = self.loki.label_values(name, span)
            except LogKitError:
                values[name] = []
            try:
                counts[name] = self._value_counts(name, span)
            except LogKitError:
                counts[name] = {}

        note = (
            "label_values come from the index and can include values with no lines in this window; "
            "counts_in_window is what is actually arriving"
        )
        # `"level" in counts` rather than a truthiness test: an empty count dict is the strongest form
        # of this signal - nothing logged at any level - and `{}` is falsy, so it would be missed.
        if "level" in counts and not any(counts["level"].values()):
            note += " - and nothing is, which points at the collector rather than at a quiet system"
        return {
            "loki_url": self.loki.url,
            "window": str(span),
            "labels": labels,
            "label_values": values,
            "counts_in_window": counts,
            "note": note,
        }

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
        # trace_id is structured metadata: filterable without `| json`, so the store applies it against
        # the index rather than this parsing every line in the window to find one id.
        query = f'{{app=~".+"}} | trace_id = {_logql_string(trace_id)}'
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

    # ------------------------------------------------------------------ lookups by key

    def _metadata_lines(self, key: str, value: str, window: str, limit: int) -> tuple[list[dict], timedelta]:
        """Lines matching one structured-metadata key, oldest first.

        Filtered by the store rather than with `| json`, because these fields are structured metadata:
        the filter is applied against the index instead of parsing every line in the window. That is what
        makes a lookup cheap enough to run while a sweep is in progress, and it is why a lookup does not
        silently depend on the line being valid JSON.
        """
        span = parse_window(window)
        selector = f'{{app=~".+"}} | {key} = {_logql_string(value)}'
        body = self.loki.query_range(selector, span, limit=limit, direction="forward")
        return [parsed_line(entry) for entry in streams_to_lines(body, limit=limit)], span

    def run_timeline(self, run_id: int, window: str = "7d", limit: int = 500) -> dict:
        """Everything one run did, in order, with the numbers that explain it.

        A run is one company's scrape - or one application, once apply-app logs these fields - and it
        spans three processes: the batch that chose it, the scrape that performed it, and the agent
        whose every step is logged. The summary is what you would otherwise reconstruct by hand.
        """
        if not isinstance(run_id, int) or run_id <= 0:
            raise LogKitError(f"run_id must be a positive integer, got {run_id!r}")
        lines, span = self._metadata_lines("run_id", str(run_id), window, limit)
        return _summarise_run(run_id, lines, span)

    def sweep_timeline(self, sweep_id: int, window: str = "7d", limit: int = 3000) -> dict:
        """One sweep: what it planned, which companies it skipped before starting and why, and how each
        company it did run turned out. The per-company rows are the point - a sweep is a batch, and the
        question is almost always 'which ones failed, and were they the same ones as last time'."""
        if not isinstance(sweep_id, int) or sweep_id <= 0:
            raise LogKitError(f"sweep_id must be a positive integer, got {sweep_id!r}")
        lines, span = self._metadata_lines("sweep_id", str(sweep_id), window, limit)
        return _summarise_sweep(sweep_id, lines, span)

    def company_history(self, company: str, window: str = "30d", limit: int = 3000) -> dict:
        """Every run for one company, newest first.

        The shape is per-run rows rather than a timeline, because the question is usually whether this
        company behaves the same way every time or something changed - a company that times out on every
        sweep is a different problem from one that failed once.
        """
        company = (company or "").strip()
        if not company:
            raise LogKitError("company must be a non-empty company slug")
        lines, span = self._metadata_lines("company", company, window, limit)
        return _summarise_company(company, lines, span)

    def listing_story(self, listing_id: int, window: str = "30d", limit: int = 500) -> dict:
        """One job listing across apps: where it was found, and every application made from it.

        This is the cross-app join. jobs-app logs `listing stored` with the id, and apply-app stores and
        logs the same listing_id, so one query spans both - which works only because the two agree on the
        field name and on what the id means.
        """
        if not isinstance(listing_id, int) or listing_id <= 0:
            raise LogKitError(f"listing_id must be a positive integer, got {listing_id!r}")
        lines, span = self._metadata_lines("listing_id", str(listing_id), window, limit)
        return _summarise_listing(listing_id, lines, span)
