"""tracekit: the analysis engine behind the trace MCP server.

Reads the scraper's run history and the browser-use traces those runs produced, from either a local
``jobs.db`` plus data directory or a live jobs-app API.

Fetching and analysis are deliberately separate. ``Lab.sync`` copies traces into a local cache once;
every other function reads that cache. That split is the whole point of this tool: an agent can ask a
broad question ("where does the agent waste time across 600 runs?") without pulling 600 traces through
its own context, and can re-ask a narrower one for free.

Sources
-------
``local``   jobs.db + <data>/traces + <data>/jobs.  Fast, offline, but only as fresh as the last sync.
``remote``  the jobs-app HTTP API.                  The live corpus, fetched once into the cache.

Stdlib only, on purpose: this runs with whatever ``python3`` is on PATH. The repo's browser venv is
pinned to a Homebrew Python that a routine ``brew upgrade`` deleted, and a tool whose only job is to
answer questions should not be the next thing that breaks.
"""

from __future__ import annotations

import glob
import json
import os
import re
import sqlite3
import statistics
import urllib.error
import urllib.request
from datetime import datetime, timezone

# Step fields a caller may search. `actions` is searched as its JSON text, which is how a caller asks
# "which runs clicked something called Submit" without a schema change.
STEP_FIELDS = ("url", "next_goal", "thinking", "evaluation_previous_goal", "memory", "actions")

DEFAULT_API = "https://jobapp.siggy-lab.org"
REMOTE_PAGE = 100  # the API caps limit at 100
REMOTE_MAX_PAGES = 40  # 4000 runs; the live table is ~600 and growing slowly


class TraceKitError(RuntimeError):
    """A failure worth showing the caller verbatim."""


def _repo_root() -> str:
    """The jobs-app checkout this file lives in (tools/tracemcp/tracekit.py -> repo root)."""
    return os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def _parse_ts(value: str | None) -> datetime | None:
    if not value:
        return None
    text = value.strip().replace("Z", "+00:00")
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        return None
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)


def _now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _percentiles(values: list[float]) -> dict:
    """min/p25/median/p75/p90/max, rounded, for a possibly empty list."""
    if not values:
        return {}
    ordered = sorted(values)

    def at(fraction: float) -> float:
        return round(ordered[min(len(ordered) - 1, int(len(ordered) * fraction))], 1)

    return {
        "min": round(ordered[0], 1),
        "p25": at(0.25),
        "median": round(statistics.median(ordered), 1),
        "p75": at(0.75),
        "p90": at(0.90),
        "max": round(ordered[-1], 1),
    }


class Lab:
    """One configured view of the pipeline's runs and traces."""

    def __init__(
        self,
        source: str | None = None,
        db: str | None = None,
        data: str | None = None,
        api: str | None = None,
        cache: str | None = None,
    ) -> None:
        root = _repo_root()
        self.source = (source or os.environ.get("TRACE_SOURCE") or "local").lower()
        self.db_path = db or os.environ.get("JOBS_DB") or os.path.join(root, "jobs.db")
        self.data_dir = data or os.environ.get("JOBS_DATA") or os.path.join(root, "data")
        self.api = (api or os.environ.get("JOBS_API") or DEFAULT_API).rstrip("/")
        self.cache_dir = cache or os.environ.get("TRACE_CACHE") or os.path.join(root, ".tracecache")

    # ---------------------------------------------------------------- cache paths

    @property
    def _runs_snapshot(self) -> str:
        return os.path.join(self.cache_dir, "runs.json")

    def _trace_path(self, run_id: int) -> str:
        return os.path.join(self.cache_dir, "traces", f"{int(run_id)}.jsonl")

    def _log_path(self, run_id: int) -> str:
        return os.path.join(self.cache_dir, "logs", f"{int(run_id)}.log")

    def _ensure_cache(self) -> None:
        os.makedirs(os.path.join(self.cache_dir, "traces"), exist_ok=True)
        os.makedirs(os.path.join(self.cache_dir, "logs"), exist_ok=True)

    # ---------------------------------------------------------------- runs

    def runs(self, refresh: bool = False) -> list[dict]:
        """Every known run, oldest first. A cached snapshot is reused unless refresh is asked for."""
        if self.source == "remote":
            if refresh or not os.path.exists(self._runs_snapshot):
                self._refresh_snapshot()
            with open(self._runs_snapshot, encoding="utf-8") as handle:
                return json.load(handle)["runs"]
        return self._local_runs()

    def _refresh_snapshot(self) -> None:
        runs: list[dict] = []
        for page in range(REMOTE_MAX_PAGES):
            body = self._http_json(f"/api/runs?limit={REMOTE_PAGE}&offset={page * REMOTE_PAGE}")
            batch = body.get("runs") or []
            runs.extend(self._normalise_remote_run(row) for row in batch)
            if len(batch) < REMOTE_PAGE:
                break
        self._ensure_cache()
        with open(self._runs_snapshot, "w", encoding="utf-8") as handle:
            json.dump({"source": self.api, "fetched_at": _now_iso(), "runs": runs}, handle)

    @staticmethod
    def _normalise_remote_run(row: dict) -> dict:
        # The API's run rows carry no company; the log's summary line and the trace do, and sync()
        # fills it in from the trace when it can. Until then it is honestly unknown.
        return {
            "id": row.get("id"),
            "platform": row.get("platform") or "",
            "status": row.get("status") or "",
            "started_at": row.get("started_at"),
            "finished_at": row.get("finished_at"),
            "items_found": row.get("items_found") or 0,
            "items_inserted": row.get("items_inserted") or 0,
            "items_updated": row.get("items_updated") or 0,
            "error_text": row.get("error_text") or "",
            "company": "",
        }

    def _local_runs(self) -> list[dict]:
        if not os.path.exists(self.db_path):
            raise TraceKitError(f"no database at {self.db_path} (source=local)")
        # Read-only: this tool never writes to the pipeline's database.
        con = sqlite3.connect(f"file:{self.db_path}?mode=ro", uri=True)
        try:
            # company_id arrived in migration 016, and an older checkout's jobs.db will not have it.
            columns = {row[1] for row in con.execute("PRAGMA table_info(scrape_runs)")}
            company_select = (
                "COALESCE(c.slug, '')"
                if "company_id" in columns
                else "''"
            )
            join = "LEFT JOIN companies c ON c.id = r.company_id" if "company_id" in columns else ""
            rows = con.execute(
                f"""
                SELECT r.id, COALESCE(p.name, ''), r.status, r.started_at, r.finished_at,
                       r.items_found, r.items_inserted, r.items_updated,
                       COALESCE(r.error_text, ''), {company_select}
                  FROM scrape_runs r
                  LEFT JOIN platforms p ON p.id = r.platform_id
                  {join}
                 ORDER BY r.id
                """
            ).fetchall()
        finally:
            con.close()
        return [
            {
                "id": row[0],
                "platform": row[1],
                "status": row[2],
                "started_at": row[3],
                "finished_at": row[4],
                "items_found": row[5],
                "items_inserted": row[6],
                "items_updated": row[7],
                "error_text": row[8] or "",
                "company": row[9] or "",
            }
            for row in rows
        ]

    # ---------------------------------------------------------------- filtering

    def query(self, runs: list[dict] | None = None, **filters) -> list[dict]:
        """Filter and sort runs. Unknown filters raise, so a typo cannot silently return everything."""
        known = {
            "platform", "status", "company", "error_contains", "min_duration_s", "max_duration_s",
            "since", "until", "min_id", "max_id", "has_trace", "sort", "limit", "offset",
        }
        unknown = set(filters) - known
        if unknown:
            raise TraceKitError(f"unknown filter(s): {', '.join(sorted(unknown))}")

        rows = list(runs if runs is not None else self.runs())
        for row in rows:
            row["duration_s"] = self._duration(row)

        def keep(row: dict) -> bool:
            if filters.get("platform") and row["platform"] != filters["platform"]:
                return False
            if filters.get("status") and row["status"] != filters["status"]:
                return False
            if filters.get("company"):
                # The API's run rows carry no company, so it is recovered from the run's own log
                # (`company=<slug>`), which sync_traces caches. Until a run has been synced its company
                # is genuinely unknown, and a company filter honestly excludes it rather than guessing.
                name = row.get("company") or self._company_from_log_file(row["id"])
                if filters["company"].lower() not in name.lower():
                    return False
            if filters.get("error_contains"):
                needle = filters["error_contains"].lower()
                if needle not in (row["error_text"] or "").lower():
                    return False
            duration = row["duration_s"]
            if filters.get("min_duration_s") is not None:
                if duration is None or duration < filters["min_duration_s"]:
                    return False
            if filters.get("max_duration_s") is not None:
                if duration is None or duration > filters["max_duration_s"]:
                    return False
            if filters.get("since") and (row["started_at"] or "") < filters["since"]:
                return False
            if filters.get("until") and (row["started_at"] or "") > filters["until"]:
                return False
            if filters.get("min_id") is not None and (row["id"] or 0) < filters["min_id"]:
                return False
            if filters.get("max_id") is not None and (row["id"] or 0) > filters["max_id"]:
                return False
            if filters.get("has_trace") is not None:
                if (self.has_trace(row["id"])) != bool(filters["has_trace"]):
                    return False
            return True

        rows = [row for row in rows if keep(row)]

        sort = filters.get("sort") or "id_desc"
        if sort == "duration_desc":
            rows.sort(key=lambda r: (r["duration_s"] is None, -(r["duration_s"] or 0)))
        elif sort == "duration_asc":
            rows.sort(key=lambda r: (r["duration_s"] is None, r["duration_s"] or 0))
        elif sort == "id_asc":
            rows.sort(key=lambda r: r["id"] or 0)
        elif sort == "started_desc":
            rows.sort(key=lambda r: r["started_at"] or "", reverse=True)
        elif sort == "id_desc":
            rows.sort(key=lambda r: r["id"] or 0, reverse=True)
        else:
            raise TraceKitError(f"unknown sort {sort!r}")

        offset = int(filters.get("offset") or 0)
        limit = filters.get("limit")
        rows = rows[offset:]
        if limit:
            rows = rows[: int(limit)]
        return rows

    @staticmethod
    def _duration(row: dict) -> float | None:
        start = _parse_ts(row.get("started_at"))
        end = _parse_ts(row.get("finished_at"))
        if start is None or end is None:
            return None
        return (end - start).total_seconds()

    # ---------------------------------------------------------------- traces

    def _trace_source_path(self, run_id: int) -> str | None:
        """Where this run's trace is readable from, or None.

        A remote trace lives in the cache, because fetching is what puts it there. A local trace is
        read where the pipeline already wrote it, so a local checkout needs no sync step - copying a
        file that is already on disk to analyse it would be busywork.
        """
        cached = self._trace_path(run_id)
        if os.path.exists(cached):
            return cached
        if self.source != "remote":
            local = os.path.join(self.data_dir, "traces", f"{int(run_id)}.jsonl")
            if os.path.exists(local):
                return local
        return None

    def has_trace(self, run_id: int) -> bool:
        return self._trace_source_path(run_id) is not None

    def trace(self, run_id: int) -> list[dict] | None:
        """The trace events, or None when this run's trace is not available. An empty list means it
        was found and the run produced no events at all — a real and common outcome, and one worth
        telling apart from "never fetched"."""
        path = self._trace_source_path(run_id)
        if path is None:
            return None
        events: list[dict] = []
        with open(path, encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if not line:
                    continue
                try:
                    events.append(json.loads(line))
                except json.JSONDecodeError:
                    continue
        return events

    def log(self, run_id: int) -> str | None:
        """The run's log. A remote log is cached on first read; a local one is read from the data dir
        in place, because the pipeline already wrote it there."""
        if self.source == "remote":
            path = self._log_path(run_id)
            if os.path.exists(path):
                with open(path, encoding="utf-8") as handle:
                    return handle.read()
            body = self._http_json(f"/api/pipeline/{int(run_id)}/log")
            text = body.get("log") or ""
            self._ensure_cache()
            with open(path, "w", encoding="utf-8") as handle:
                handle.write(text)
            return text
        path = os.path.join(self.data_dir, "jobs", f"{int(run_id)}.log")
        if os.path.exists(path):
            with open(path, encoding="utf-8") as handle:
                return handle.read()
        return None

    def sync(self, runs: list[dict], force: bool = False) -> dict:
        """Copy traces (and logs) for these runs into the cache. Idempotent: a cached trace is reused
        unless force is set, so this is safe to call broadly and cheap to call again."""
        self._ensure_cache()
        fetched = cached = missing = failed = 0
        for row in runs:
            run_id = row["id"]
            if not run_id:
                continue

            # The log carries the company and the fetch summary the trace cannot, so it is always
            # worth having; it is also small next to a trace.
            log_text = None
            try:
                log_text = self.log(run_id)
            except TraceKitError:
                pass
            if not row.get("company"):
                row["company"] = self._company_from_text(log_text) or ""

            if self.has_trace(run_id) and not force:
                cached += 1
                if not row["company"]:
                    row["company"] = self._company_from_events(self.trace(run_id) or [])
                continue

            if self.source != "remote":
                # A local trace is read where it lies, so there is nothing to fetch: either it is
                # there (already counted above, unless force asked to re-read it) or it does not exist.
                if self.has_trace(run_id):
                    cached += 1
                else:
                    missing += 1
                continue

            try:
                events = self._fetch_remote_trace(run_id)
            except TraceKitError:
                failed += 1
                continue
            if events is None:
                missing += 1
                continue
            # A fetched trace is written even when it is empty: the empty file is what records that
            # this run was looked at and had nothing, so a second sync does not refetch it.
            with open(self._trace_path(run_id), "w", encoding="utf-8") as handle:
                for event in events:
                    handle.write(json.dumps(event, ensure_ascii=False, default=str) + "\n")
            if not row["company"]:
                row["company"] = self._company_from_events(events)
            fetched += 1

        # Persist what the logs taught us, so a later company filter does not have to re-read them.
        if self.source == "remote" and os.path.exists(self._runs_snapshot):
            try:
                with open(self._runs_snapshot, encoding="utf-8") as handle:
                    snapshot = json.load(handle)
                known = {row["id"]: row.get("company") or "" for row in runs}
                for entry in snapshot.get("runs", []):
                    if not entry.get("company") and known.get(entry.get("id")):
                        entry["company"] = known[entry["id"]]
                with open(self._runs_snapshot, "w", encoding="utf-8") as handle:
                    json.dump(snapshot, handle)
            except (OSError, json.JSONDecodeError):
                pass

        return {
            "fetched": fetched,
            "already_cached": cached,
            "not_found": missing,
            "failed": failed,
            "cache_dir": self.cache_dir,
        }

    def _fetch_remote_trace(self, run_id: int) -> list[dict] | None:
        body = self._http_json(f"/api/traces/{int(run_id)}")
        if not body.get("present"):
            return None
        return body.get("events") or []

    @staticmethod
    def _company_from_events(events: list[dict]) -> str:
        """The company is not on the API's run rows; a resolution event names it when the writer knew."""
        for event in events:
            if event.get("event") == "resolution" and event.get("company"):
                return str(event["company"])
        return ""

    def log_for_run(self, run_id: int) -> tuple[str, str]:
        """The most useful log text for a run, and where it came from.

        A per-company run during a sweep has an *empty* log of its own: `batch` runs the company's
        `scrape` as a child process, which inherits the batch's stdout, so the whole sweep's output
        lands in the batch run's file. Asking for the child's log and getting nothing is therefore the
        normal case, not a missing file - the answer is the lines the parent captured. Those lines are
        prefixed with `run_id=<id>`, which is what makes the attribution exact rather than a guess.
        """
        own = self.log(run_id)
        if own and own.strip():
            return own, "own"

        pattern = re.compile(rf"\brun_id={int(run_id)}\b")
        for candidate in self._cached_batch_logs():
            try:
                text = self.log(candidate)
            except TraceKitError:
                continue
            if not text:
                continue
            lines = [line for line in text.splitlines() if pattern.search(line)]
            if lines:
                return "\n".join(lines) + "\n", f"parent_batch:{candidate}"
        return "", "not_found"

    def _cached_batch_logs(self) -> list[int]:
        """Cached logs belonging to sweep runs, newest first. A sweep run is recognised by its own
        platform (when known) or by the batch banner, since the API's rows do not always name it."""
        candidates: list[int] = []
        for row in self.runs():
            if row.get("platform") == "career_batch" and row["id"]:
                candidates.append(row["id"])
        candidates.sort(reverse=True)
        present = [c for c in candidates if os.path.exists(self._log_path(c))]
        if present:
            return present
        # Fall back to whatever cached log carries a batch banner, which is what an older snapshot
        # without the platform name leaves us.
        for path in sorted(glob.glob(os.path.join(self.cache_dir, "logs", "*.log")), reverse=True):
            if os.path.getsize(path) == 0:
                continue
            try:
                with open(path, encoding="utf-8", errors="replace") as handle:
                    if handle.readline().startswith("batch:"):
                        candidates.append(int(os.path.basename(path).split(".")[0]))
            except (OSError, ValueError):
                continue
        return candidates

    def sweep_outcomes(self, batch_run_id: int | None = None) -> dict:
        """Per-company outcomes from a sweep's log.

        This is the view an optimisation question needs and a trace cannot give: a trace covers one
        company's agent phase, while this classifies the *whole sweep* - how many companies were
        skipped for having nothing, how many failed, and how many actually reached a fetch.
        """
        run_id = batch_run_id
        if run_id is None:
            sweeps = [r["id"] for r in self.runs() if r.get("platform") == "career_batch"]
            sweeps.sort(reverse=True)
            run_id = next(
                (s for s in sweeps if os.path.exists(self._log_path(s)) and os.path.getsize(self._log_path(s)) > 0),
                sweeps[0] if sweeps else None,
            )
        if run_id is None:
            raise TraceKitError("no sweep run found; pass batch_run_id")

        text = self.log(run_id)
        if text is None:
            raise TraceKitError(f"run {run_id} has no cached log")
        if not text.strip():
            raise TraceKitError(
                f"run {run_id}'s log is empty; the sweep's output is in the batch run's log, so pass "
                f"that run's id (or sync the career_batch runs first)"
            )

        # The batch prints one banner per company, then that company's lines.
        blocks = re.split(r"^\[(\d+)/(\d+)\] company=(\S+) vendor=(\S+)\s*$", text, flags=re.MULTILINE)
        outcomes: dict[str, list[str]] = {}
        totals = {"found": 0, "inserted": 0, "closed": 0}
        seen = 0
        # blocks: [preamble, index, total, slug, vendor, body, index, total, slug, vendor, body, ...]
        for i in range(1, len(blocks), 5):
            slug = blocks[i + 2]
            body = blocks[i + 4] if i + 4 < len(blocks) else ""
            seen += 1
            if "agent_failed=1" in body:
                outcome = "agent_failed"
            elif "robots=disallowed" in body:
                outcome = "robots_disallowed"
            elif "listings=none" in body:
                outcome = "listings_none"
            elif "no_remote_roles=true" in body:
                outcome = "no_remote_roles"
            elif "found=" in body:
                outcome = "fetched"
            else:
                outcome = "incomplete"
            outcomes.setdefault(outcome, []).append(slug)
            for key in totals:
                found = re.search(rf"\b{key}=(\d+)", body)
                if found:
                    totals[key] += int(found.group(1))

        company_total = blocks[2] if len(blocks) > 2 else ""
        return {
            "batch_run_id": run_id,
            "companies_in_log": seen,
            "sweep_size": int(company_total) if company_total.isdigit() else None,
            "outcomes": {
                name: {"count": len(slugs), "companies": slugs[:25]}
                for name, slugs in sorted(outcomes.items(), key=lambda kv: -len(kv[1]))
            },
            "totals": totals,
            "note": (
                "a trace covers one company's agent phase; these outcomes cover the whole sweep. "
                "'no_remote_roles' and 'fetched' are the companies the listings-URL cache now covers, "
                "so they stop costing an agent run; 'agent_failed', 'listings_none' and "
                "'robots_disallowed' are not cached and run again every sweep."
            ),
        }

    @staticmethod
    def _company_from_text(text: str | None) -> str:
        """The scraper's summary line carries `company=<slug>`, which is where the company is
        recoverable for a run whose row does not have it."""
        match = re.search(r"\bcompany=([A-Za-z0-9._-]+)", text or "")
        return match.group(1) if match else ""

    def _company_from_log_file(self, run_id: int) -> str:
        """Recover a company from an already-cached log, without fetching one: a filter must not turn
        into network traffic. Both locations are tried, because a remote log is cached and a local one
        stays in the data directory."""
        for path in (self._log_path(run_id), os.path.join(self.data_dir, "jobs", f"{int(run_id)}.log")):
            if not os.path.exists(path):
                continue
            try:
                with open(path, encoding="utf-8") as handle:
                    found = self._company_from_text(handle.read(20000))
            except OSError:
                continue
            if found:
                return found
        return ""

    def _http_json(self, path: str) -> dict:
        url = f"{self.api}{path}"
        request = urllib.request.Request(url, headers={"Accept": "application/json"})
        try:
            with urllib.request.urlopen(request, timeout=45) as response:
                return json.load(response)
        except urllib.error.HTTPError as exc:
            raise TraceKitError(f"{url} -> HTTP {exc.code}") from exc
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise TraceKitError(f"{url} -> {exc}") from exc

    # ---------------------------------------------------------------- analysis

    def stats(self, runs: list[dict], with_traces: bool = False) -> dict:
        """The shape of a run population, so a broad question is one call rather than a thousand."""
        durations = [r["duration_s"] for r in runs if r.get("duration_s") is not None]
        by_status: dict[str, int] = {}
        by_platform: dict[str, int] = {}
        errors: dict[str, int] = {}
        for row in runs:
            by_status[row["status"]] = by_status.get(row["status"], 0) + 1
            by_platform[row["platform"]] = by_platform.get(row["platform"], 0) + 1
            if row["status"] == "error":
                key = (row["error_text"] or "(no error text)")[:70]
                errors[key] = errors.get(key, 0) + 1

        out: dict = {
            "runs": len(runs),
            "duration_s": _percentiles(durations),
            "total_wall_hours": round(sum(durations) / 3600, 1),
            "status": dict(sorted(by_status.items(), key=lambda kv: -kv[1])),
            "platform": dict(sorted(by_platform.items(), key=lambda kv: -kv[1])),
            "top_errors": dict(sorted(errors.items(), key=lambda kv: -kv[1])[:10]),
            "traced": sum(1 for r in runs if self.has_trace(r["id"])),
            "runs_without_duration": sum(1 for r in runs if r.get("duration_s") is None),
        }
        if with_traces:
            out["steps"] = self.step_stats(runs)
        return out

    def step_stats(self, runs: list[dict]) -> dict:
        """Step and action statistics over the cached traces of these runs."""
        steps_per_run: list[int] = []
        actions: dict[str, int] = {}
        latencies: list[tuple[float, int, int]] = []
        done = success = 0
        considered = 0
        for row in runs:
            events = self.trace(row["id"])
            if events is None:
                continue
            considered += 1
            steps = [e for e in events if e.get("event") == "step"]
            steps_per_run.append(len(steps))
            for event in steps:
                for action in event.get("actions") or []:
                    if isinstance(action, dict) and action:
                        actions[next(iter(action))] = actions.get(next(iter(action)), 0) + 1
            # Per-step latency needs timestamps, which only traces written after the trace writer
            # started emitting `ts` carry. Older traces simply contribute no latency.
            stamps = [(_parse_ts(e.get("ts")), e.get("step")) for e in steps if e.get("ts")]
            for index in range(1, len(stamps)):
                previous, current = stamps[index - 1][0], stamps[index][0]
                if previous and current:
                    latencies.append(((current - previous).total_seconds(), row["id"], stamps[index][1]))
            for event in events:
                if event.get("event") == "done":
                    done += 1
                    if event.get("success"):
                        success += 1

        buckets = {"0": 0, "1-3": 0, "4-8": 0, "9-15": 0, "16-25": 0, "26+": 0}
        for count in steps_per_run:
            if count == 0:
                buckets["0"] += 1
            elif count <= 3:
                buckets["1-3"] += 1
            elif count <= 8:
                buckets["4-8"] += 1
            elif count <= 15:
                buckets["9-15"] += 1
            elif count <= 25:
                buckets["16-25"] += 1
            else:
                buckets["26+"] += 1

        latencies.sort(reverse=True)
        return {
            "traces_analyzed": considered,
            "steps_per_run": _percentiles([float(c) for c in steps_per_run]) if steps_per_run else {},
            "step_buckets": buckets,
            "actions": dict(sorted(actions.items(), key=lambda kv: -kv[1])),
            "with_done_event": done,
            "done_successful": success,
            "step_latency_s": _percentiles([l[0] for l in latencies]) if latencies else {},
            "slowest_steps": [
                {"run_id": run_id, "step": step, "latency_s": round(latency, 1)}
                for latency, run_id, step in latencies[:10]
            ],
            "latency_note": (
                "" if latencies
                else "no per-step timing in these traces: they predate the `ts` field on trace events"
            ),
        }

    def digest(self, run_id: int, max_steps: int = 40, include_text: bool = False) -> dict:
        """One trace, in the shape an optimiser needs: what it did, where, and how it ended."""
        run = next((r for r in self.runs() if r["id"] == run_id), {"id": run_id})
        events = self.trace(run_id)
        if events is None:
            raise TraceKitError(
                f"run {run_id} has no cached trace; call sync_traces for it first "
                f"(cache: {self.cache_dir})"
            )
        steps = [e for e in events if e.get("event") == "step"]
        actions: dict[str, int] = {}
        for event in steps:
            for action in event.get("actions") or []:
                if isinstance(action, dict) and action:
                    name = next(iter(action))
                    actions[name] = actions.get(name, 0) + 1
        urls = [e.get("url") for e in steps if e.get("url")]
        done = next((e for e in events if e.get("event") == "done"), None)

        detail = []
        for event in steps[:max_steps]:
            entry = {
                "step": event.get("step"),
                "url": event.get("url"),
                "actions": [next(iter(a)) for a in (event.get("actions") or []) if isinstance(a, dict) and a],
            }
            goal = event.get("next_goal") or event.get("evaluation_previous_goal")
            if goal:
                entry["goal"] = goal if include_text else str(goal)[:200]
            if event.get("ts"):
                entry["ts"] = event["ts"]
            detail.append(entry)

        return {
            "run_id": run_id,
            "company": run.get("company") or "",
            "status": run.get("status"),
            "duration_s": self._duration(run) if run.get("started_at") else None,
            "error_text": run.get("error_text") or "",
            "steps": len(steps),
            "events": len(events),
            "distinct_urls": len(set(urls)),
            "actions": dict(sorted(actions.items(), key=lambda kv: -kv[1])),
            "first_url": urls[0] if urls else "",
            "last_url": urls[-1] if urls else "",
            "has_done_event": done is not None,
            "done_success": bool(done.get("success")) if done else None,
            "final_result": (done.get("final_result") if done else "") or "",
            "steps_detail": detail,
            "steps_truncated": max(0, len(steps) - len(detail)),
        }

    def search_steps(
        self,
        pattern: str,
        runs: list[dict],
        fields: tuple[str, ...] = STEP_FIELDS,
        limit: int = 60,
    ) -> dict:
        """Regex search across the cached traces' step fields. This is the "find where it goes wrong"
        tool: a pattern like a URL fragment, an action name, or a phrase the agent repeats."""
        unknown = [f for f in fields if f not in STEP_FIELDS]
        if unknown:
            raise TraceKitError(f"unknown field(s): {', '.join(unknown)}; known: {', '.join(STEP_FIELDS)}")
        try:
            regex = re.compile(pattern, re.IGNORECASE)
        except re.error as exc:
            raise TraceKitError(f"bad regex {pattern!r}: {exc}") from exc

        matches: list[dict] = []
        scanned_runs = scanned_steps = 0
        for row in runs:
            events = self.trace(row["id"])
            if events is None:
                continue
            scanned_runs += 1
            for event in events:
                if event.get("event") != "step":
                    continue
                scanned_steps += 1
                for field in fields:
                    value = event.get(field)
                    if value is None:
                        continue
                    text = value if isinstance(value, str) else json.dumps(value, default=str)
                    found = regex.search(text)
                    if not found:
                        continue
                    start = max(0, found.start() - 60)
                    matches.append({
                        "run_id": row["id"],
                        "step": event.get("step"),
                        "field": field,
                        "url": event.get("url") or "",
                        "snippet": text[start:found.end() + 100].replace("\n", " "),
                    })
                    break  # one hit per step is enough to locate it
                if len(matches) >= limit:
                    return {
                        "scanned_runs": scanned_runs,
                        "scanned_steps": scanned_steps,
                        "matches": matches,
                        "truncated": True,
                    }
        return {
            "scanned_runs": scanned_runs,
            "scanned_steps": scanned_steps,
            "matches": matches,
            "truncated": False,
        }
