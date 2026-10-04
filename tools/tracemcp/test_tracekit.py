"""Tests for the trace analysis engine.

Everything runs against synthetic runs, traces and logs in a temporary directory, so the suite needs
neither the live API nor the real database. The local-source path is exercised against a minimal
SQLite file, which is also what pins the migration-016 behaviour: a database without
``scrape_runs.company_id`` must still list runs rather than fail.
"""

from __future__ import annotations

import json
import os
import shutil
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from tracekit import STEP_FIELDS, Lab, TraceKitError  # noqa: E402


def _write_jsonl(path: str, events: list[dict]) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        for event in events:
            handle.write(json.dumps(event) + "\n")


class LabTestCase(unittest.TestCase):
    """A local source over a temp DB, temp data dir and temp cache."""

    with_company_column = True

    def setUp(self) -> None:
        self.root = tempfile.mkdtemp(prefix="tracekit-test-")
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)
        self.db = os.path.join(self.root, "jobs.db")
        self.data = os.path.join(self.root, "data")
        self.cache = os.path.join(self.root, "cache")
        os.makedirs(os.path.join(self.data, "traces"), exist_ok=True)
        os.makedirs(os.path.join(self.data, "jobs"), exist_ok=True)
        self._make_db()
        self.lab = Lab(source="local", db=self.db, data=self.data, cache=self.cache)

    def _make_db(self) -> None:
        # The only difference between a current database and a pre-migration-016 one is this column.
        if self.with_company_column:
            columns = "id, platform_id, started_at, finished_at, status, error_text, company_id"
            company_column = ", company_id INTEGER"
            values = (
                "(1, 27, '2026-10-01T00:00:00Z', '2026-10-01T00:00:30Z', 'ok', NULL, 1),"
                "(2, 27, '2026-10-02T00:00:00Z', '2026-10-02T00:12:00Z', 'error', 'agent timed out', 2),"
                "(3, 28, '2026-10-03T00:00:00Z', '2026-10-03T05:00:00Z', 'ok', NULL, NULL)"
            )
        else:
            columns = "id, platform_id, started_at, finished_at, status, error_text"
            company_column = ""
            values = (
                "(1, 27, '2026-10-01T00:00:00Z', '2026-10-01T00:00:30Z', 'ok', NULL),"
                "(2, 27, '2026-10-02T00:00:00Z', '2026-10-02T00:12:00Z', 'error', 'agent timed out'),"
                "(3, 28, '2026-10-03T00:00:00Z', '2026-10-03T05:00:00Z', 'ok', NULL)"
            )

        con = sqlite3.connect(self.db)
        con.executescript(
            f"""
            CREATE TABLE platforms (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
            CREATE TABLE companies (id INTEGER PRIMARY KEY, slug TEXT NOT NULL);
            CREATE TABLE scrape_runs (
                id INTEGER PRIMARY KEY, platform_id INTEGER NOT NULL,
                started_at TEXT NOT NULL, finished_at TEXT, status TEXT NOT NULL DEFAULT 'running',
                items_found INTEGER NOT NULL DEFAULT 0, items_inserted INTEGER NOT NULL DEFAULT 0,
                items_updated INTEGER NOT NULL DEFAULT 0, error_text TEXT{company_column}
            );
            INSERT INTO platforms (id, name) VALUES (27, 'career_listings'), (28, 'career_batch');
            INSERT INTO companies (id, slug) VALUES (1, 'acme-corp'), (2, 'globex');
            INSERT INTO scrape_runs ({columns}) VALUES {values};
            """
        )
        con.commit()
        con.close()

    def _add_trace(self, run_id: int, events: list[dict]) -> None:
        _write_jsonl(os.path.join(self.data, "traces", f"{run_id}.jsonl"), events)

    def _add_log(self, run_id: int, text: str) -> None:
        with open(os.path.join(self.data, "jobs", f"{run_id}.log"), "w", encoding="utf-8") as handle:
            handle.write(text)


class TestRuns(LabTestCase):
    def test_lists_runs_with_durations(self) -> None:
        runs = self.lab.runs()
        self.assertEqual([r["id"] for r in runs], [1, 2, 3])
        self.assertEqual(self.lab._duration(runs[0]), 30.0)
        self.assertEqual(self.lab._duration(runs[1]), 720.0)
        self.assertEqual(runs[0]["company"], "acme-corp")

    def test_filters(self) -> None:
        self.assertEqual([r["id"] for r in self.lab.query(platform="career_listings", sort="id_asc")], [1, 2])
        self.assertEqual([r["id"] for r in self.lab.query(status="error")], [2])
        self.assertEqual([r["id"] for r in self.lab.query(error_contains="TIMED OUT")], [2])
        self.assertEqual([r["id"] for r in self.lab.query(min_duration_s=100, sort="id_asc")], [2, 3])
        self.assertEqual([r["id"] for r in self.lab.query(max_duration_s=100)], [1])
        self.assertEqual([r["id"] for r in self.lab.query(company="globex")], [2])

    def test_sort_and_page(self) -> None:
        self.assertEqual([r["id"] for r in self.lab.query(sort="duration_desc")], [3, 2, 1])
        self.assertEqual([r["id"] for r in self.lab.query(sort="id_asc", limit=2)], [1, 2])
        self.assertEqual([r["id"] for r in self.lab.query(sort="id_asc", offset=1)], [2, 3])

    def test_unknown_filter_and_sort_raise(self) -> None:
        with self.assertRaises(TraceKitError):
            self.lab.query(platfrom="typo")
        with self.assertRaises(TraceKitError):
            self.lab.query(sort="fastest")

    def test_stats(self) -> None:
        stats = self.lab.stats(self.lab.query(limit=None))
        self.assertEqual(stats["runs"], 3)
        self.assertEqual(stats["status"], {"ok": 2, "error": 1})
        self.assertEqual(stats["top_errors"], {"agent timed out": 1})
        self.assertEqual(stats["traced"], 0)
        self.assertEqual(stats["duration_s"]["max"], 18000.0)


class TestRunsWithoutCompanyColumn(LabTestCase):
    """An older checkout's jobs.db predates migration 016 and has no company_id."""

    with_company_column = False

    def test_listing_still_works(self) -> None:
        runs = self.lab.runs()
        self.assertEqual([r["id"] for r in runs], [1, 2, 3])
        self.assertEqual(runs[0]["company"], "")


class TestTraces(LabTestCase):
    def setUp(self) -> None:
        super().setUp()
        self._add_trace(1, [
            {"event": "step", "step": 1, "url": "https://acme.test/careers", "next_goal": "Click Search jobs",
             "actions": [{"click": {"index": 26}}], "ts": "2026-10-01T00:00:00.000Z"},
            {"event": "step", "step": 2, "url": "https://jobs.acme.test/careers", "next_goal": "Type software engineer",
             "actions": [{"input": {"text": "software engineer"}}], "ts": "2026-10-01T00:00:20.000Z"},
            {"event": "step", "step": 3, "url": "https://jobs.acme.test/careers?q=x", "next_goal": "Count roles",
             "actions": [{"scroll": {"down": True}}, {"scroll": {"down": True}}],
             "ts": "2026-10-01T00:01:20.000Z"},
            {"event": "done", "success": True, "steps": 3, "final_result": "{\"listings_url\": \"x\"}"},
        ])
        _write_jsonl(os.path.join(self.data, "traces", "2.jsonl"), [])  # fetched, produced nothing

    def test_present_empty_and_missing_are_distinct(self) -> None:
        self.assertIsNone(self.lab.trace(3))          # never fetched
        self.assertEqual(self.lab.trace(2), [])       # fetched, no events
        self.assertEqual(len(self.lab.trace(1)), 4)
        self.assertTrue(self.lab.has_trace(2))
        self.assertFalse(self.lab.has_trace(3))

    def test_has_trace_filter(self) -> None:
        self.assertEqual([r["id"] for r in self.lab.query(has_trace=True, sort="id_asc")], [1, 2])
        self.assertEqual([r["id"] for r in self.lab.query(has_trace=False, sort="id_asc")], [3])

    def test_digest(self) -> None:
        digest = self.lab.digest(1)
        self.assertEqual(digest["steps"], 3)
        self.assertEqual(digest["distinct_urls"], 3)
        self.assertEqual(digest["actions"], {"scroll": 2, "click": 1, "input": 1})
        self.assertTrue(digest["has_done_event"])
        self.assertTrue(digest["done_success"])
        self.assertEqual(digest["first_url"], "https://acme.test/careers")
        self.assertEqual(digest["last_url"], "https://jobs.acme.test/careers?q=x")
        self.assertEqual([s["actions"] for s in digest["steps_detail"]],
                         [["click"], ["input"], ["scroll", "scroll"]])

    def test_digest_without_a_trace_explains_itself(self) -> None:
        with self.assertRaises(TraceKitError) as caught:
            self.lab.digest(3)
        self.assertIn("sync_traces", str(caught.exception))

    def test_search_steps(self) -> None:
        result = self.lab.search_steps("acme\\.test/careers", self.lab.runs())
        # all three URLs contain acme.test/careers (step 3 via jobs.acme.test)
        self.assertEqual([(m["run_id"], m["step"]) for m in result["matches"]], [(1, 1), (1, 2), (1, 3)])
        self.assertFalse(result["truncated"])

        narrowed = self.lab.search_steps("Type software", self.lab.runs(), fields=("next_goal",))
        self.assertEqual([(m["run_id"], m["step"], m["field"]) for m in narrowed["matches"]], [(1, 2, "next_goal")])

    def test_search_rejects_unknown_fields_and_bad_regex(self) -> None:
        with self.assertRaises(TraceKitError):
            self.lab.search_steps("x", self.lab.runs(), fields=("nope",))
        with self.assertRaises(TraceKitError):
            self.lab.search_steps("([", self.lab.runs())

    def test_step_stats_computes_latency_from_timestamps(self) -> None:
        stats = self.lab.step_stats(self.lab.runs())
        self.assertEqual(stats["traces_analyzed"], 2)
        self.assertEqual(stats["with_done_event"], 1)
        self.assertEqual(stats["done_successful"], 1)
        self.assertEqual(stats["actions"], {"scroll": 2, "click": 1, "input": 1})
        # step 2 -> 3 took 60s; step 1 -> 2 took 20s.
        self.assertEqual(stats["slowest_steps"][0], {"run_id": 1, "step": 3, "latency_s": 60.0})
        self.assertEqual(stats["latency_note"], "")
        self.assertEqual(stats["step_buckets"], {"0": 1, "1-3": 1, "4-8": 0, "9-15": 0, "16-25": 0, "26+": 0})

    def test_step_stats_says_so_when_traces_have_no_timing(self) -> None:
        _write_jsonl(os.path.join(self.data, "traces", "3.jsonl"), [
            {"event": "step", "step": 1, "url": "u", "actions": []},
            {"event": "step", "step": 2, "url": "u", "actions": []},
        ])
        stats = self.lab.step_stats([r for r in self.lab.runs() if r["id"] == 3])
        self.assertEqual(stats["step_latency_s"], {})
        self.assertIn("predate the `ts` field", stats["latency_note"])

    def test_get_run_log_reads_and_greps(self) -> None:
        self._add_log(1, "run_id=1 company=acme-corp listings_url=cache\nrun_id=1 company=acme-corp found=0\n")
        text = self.lab.log(1)
        self.assertIn("company=acme-corp", text)
        self.assertIsNone(self.lab.log(3))

    def test_company_recovered_from_a_log_when_the_row_lacks_it(self) -> None:
        self._add_log(3, "run_id=3 company=recovered-co no_remote_roles=true\n")
        self.assertEqual([r["id"] for r in self.lab.query(company="recovered")], [3])


class FakeRemoteLab(Lab):
    """A remote source whose HTTP layer is canned, so the fetch-and-cache path is tested without a
    network. Overriding `_http_json` rather than the fetch methods keeps the real parsing, the real
    log handling and the real snapshot write in the path under test."""

    def __init__(self, root: str, traces: dict[int, list[dict]], logs: dict[int, str]) -> None:
        super().__init__(source="remote", cache=os.path.join(root, "cache"), api="http://fake.test")
        self._traces = traces
        self._logs = logs
        self.requests: list[str] = []

    def _http_json(self, path: str) -> dict:
        self.requests.append(path)
        if path.startswith("/api/traces/"):
            run_id = int(path.rsplit("/", 1)[1])
            if run_id not in self._traces:
                return {"id": run_id, "present": False, "events": []}
            return {"id": run_id, "present": True, "events": self._traces[run_id]}
        if path.startswith("/api/pipeline/"):
            run_id = int(path.split("/")[3])
            return {"id": run_id, "present": run_id in self._logs, "log": self._logs.get(run_id, "")}
        raise TraceKitError(f"unexpected request {path}")


def _remote_row(run_id: int, **overrides) -> dict:
    row = {
        "id": run_id, "platform": "career_listings", "status": "ok",
        "started_at": "2026-10-01T00:00:00Z", "finished_at": "2026-10-01T00:00:30Z",
        "items_found": 0, "items_inserted": 0, "items_updated": 0, "error_text": "", "company": "",
    }
    row.update(overrides)
    return row


class TestSync(LabTestCase):
    def test_local_source_has_nothing_to_fetch(self) -> None:
        self._add_trace(1, [{"event": "step", "step": 1, "url": "u", "actions": []}])
        result = self.lab.sync(self.lab.runs())
        self.assertEqual((result["fetched"], result["already_cached"], result["not_found"]), (0, 1, 2))
        self.assertTrue(self.lab.has_trace(1))

    def test_remote_sync_fetches_then_reuses(self) -> None:
        lab = FakeRemoteLab(
            self.root,
            traces={1: [{"event": "step", "step": 1, "url": "https://acme.test", "actions": []}], 2: []},
            logs={1: "run_id=1 company=acme-corp listings_url=agent\n"},
        )
        rows = [_remote_row(1), _remote_row(2), _remote_row(3)]

        first = lab.sync(rows)
        self.assertEqual((first["fetched"], first["already_cached"], first["not_found"]), (2, 0, 1))
        self.assertTrue(lab.has_trace(1))
        self.assertTrue(lab.has_trace(2), "a fetched-but-empty trace must still be cached")
        self.assertEqual(lab.trace(2), [])
        self.assertFalse(lab.has_trace(3), "a trace the API does not have must not be cached")

        # Second pass: nothing to do. Run 3 was absent before and is not retried into existence.
        second = lab.sync(rows)
        self.assertEqual((second["fetched"], second["already_cached"], second["not_found"]), (0, 2, 1))

    def test_remote_sync_recovers_the_company_from_the_log(self) -> None:
        lab = FakeRemoteLab(
            self.root, traces={1: [{"event": "done", "success": True}]},
            logs={1: "run_id=1 company=acme-corp found=3\n"},
        )
        rows = [_remote_row(1)]
        lab.sync(rows)
        self.assertEqual(rows[0]["company"], "acme-corp")

    def test_remote_sync_persists_recovered_companies_into_the_snapshot(self) -> None:
        lab = FakeRemoteLab(self.root, traces={}, logs={1: "run_id=1 company=acme-corp\n"})
        os.makedirs(lab.cache_dir, exist_ok=True)
        with open(lab._runs_snapshot, "w", encoding="utf-8") as handle:
            json.dump({"source": lab.api, "runs": [_remote_row(1)]}, handle)

        lab.sync([_remote_row(1)])

        with open(lab._runs_snapshot, encoding="utf-8") as handle:
            saved = json.load(handle)
        self.assertEqual(saved["runs"][0]["company"], "acme-corp")

    def test_remote_log_is_fetched_once(self) -> None:
        lab = FakeRemoteLab(self.root, traces={}, logs={1: "hello\n"})
        self.assertEqual(lab.log(1), "hello\n")
        self.assertEqual(lab.log(1), "hello\n")
        self.assertEqual(sum(1 for r in lab.requests if "/log" in r), 1)

    def test_fetch_failure_is_counted_not_raised(self) -> None:
        lab = FakeRemoteLab(self.root, traces={1: []}, logs={})
        lab._http_json = lambda path: (_ for _ in ()).throw(TraceKitError("boom"))  # type: ignore[assignment]
        result = lab.sync([_remote_row(1)])
        self.assertEqual(result["failed"], 1)
        self.assertEqual(result["fetched"], 0)


class TestRemoteRunNormalisation(unittest.TestCase):
    def test_missing_fields_become_empty_not_none(self) -> None:
        row = Lab._normalise_remote_run({"id": 7, "platform": "career_listings", "status": "ok"})
        self.assertEqual(row["id"], 7)
        self.assertEqual(row["error_text"], "")
        self.assertEqual(row["items_found"], 0)
        self.assertIsNone(row["finished_at"])


class TestSweepOutcomes(LabTestCase):
    """A sweep's log is where per-company outcomes live: the children inherit the batch's stdout, so
    their own logs are empty and this is the only place the sweep's shape is recorded."""

    BATCH_LOG = """batch: 3 companies, sequential (skip-ok=false skip-traced=false)
[1/3] company=alpha vendor=workday
run_id=10 company=alpha agent: has_remote=false count=0 evidence="none"
run_id=10 company=alpha no_remote_roles=true found=0 evidence="none"
[2/3] company=beta vendor=greenhouse
run_id=11 company=beta agent: has_remote=true count=3 evidence="three"
run_id=11 company=beta listings=https://x resolution=agent found=5 inserted=2 skipped=1 closed=1
[3/3] company=gamma vendor=lever
run_id=12 company=gamma agent_failed=1 error="timeout" timeout=true
"""

    def setUp(self) -> None:
        super().setUp()
        # Run 3 is the local fixture's career_batch run.
        self._add_log(3, self.BATCH_LOG)

    def test_classifies_each_company(self) -> None:
        got = self.lab.sweep_outcomes(3)
        self.assertEqual(got["companies_in_log"], 3)
        self.assertEqual(got["sweep_size"], 3)
        self.assertEqual(
            {name: entry["count"] for name, entry in got["outcomes"].items()},
            {"no_remote_roles": 1, "fetched": 1, "agent_failed": 1},
        )
        self.assertEqual(got["outcomes"]["fetched"]["companies"], ["beta"])
        self.assertEqual(got["totals"], {"found": 5, "inserted": 2, "closed": 1})

    def test_defaults_to_the_newest_sweep(self) -> None:
        got = self.lab.sweep_outcomes()
        self.assertEqual(got["batch_run_id"], 3)

    def test_a_per_company_run_reads_its_parent_batch_lines(self) -> None:
        text, origin = self.lab.log_for_run(11)
        self.assertEqual(origin, "parent_batch:3")
        self.assertIn("found=5", text)
        self.assertNotIn("company=alpha", text, "only the requested run's lines")

    def test_a_run_with_its_own_log_uses_it(self) -> None:
        self._add_log(1, "run_id=1 company=acme-corp listings_url=cache\n")
        text, origin = self.lab.log_for_run(1)
        self.assertEqual(origin, "own")
        self.assertIn("listings_url=cache", text)

    def test_an_unknown_run_reports_not_found(self) -> None:
        text, origin = self.lab.log_for_run(999)
        self.assertEqual((text, origin), ("", "not_found"))

    def test_an_empty_sweep_log_explains_itself(self) -> None:
        self._add_log(1, "")  # run 1 is career_listings, so there is no sweep log to fall back to
        with self.assertRaises(TraceKitError) as caught:
            self.lab.sweep_outcomes(2)
        self.assertIn("has no cached log", str(caught.exception))


class TestPercentiles(unittest.TestCase):
    def test_empty(self) -> None:
        from tracekit import _percentiles

        self.assertEqual(_percentiles([]), {})

    def test_known_values(self) -> None:
        from tracekit import _percentiles

        got = _percentiles([float(n) for n in range(1, 101)])
        self.assertEqual(got["min"], 1.0)
        self.assertEqual(got["median"], 50.5)
        self.assertEqual(got["max"], 100.0)


class TestStepFieldVocabulary(unittest.TestCase):
    def test_fields_match_what_the_writer_emits(self) -> None:
        # The vocabulary is a contract with worker/agent_trace.py; a rename there must fail here.
        self.assertEqual(
            set(STEP_FIELDS),
            {"url", "next_goal", "thinking", "evaluation_previous_goal", "memory", "actions"},
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
