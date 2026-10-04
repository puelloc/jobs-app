"""Tests for the log query engine.

Most of the suite runs against a canned Loki, so it needs no store and no network. One class at the end
runs against a real Loki when one is reachable (LOKI_URL, or localhost:3100) and skips itself
otherwise, which is how the LogQL these tools build gets checked against a real parser rather than
against this file's assumptions.
"""

from __future__ import annotations

import json
import os
import sys
import unittest
import urllib.error
from datetime import timedelta

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import logkit  # noqa: E402
from logkit import LogKit, LogKitError, Loki, parsed_line, parse_window, streams_to_lines  # noqa: E402


def _ns(seconds_ago: float) -> str:
    import time

    return str(int((time.time() - seconds_ago) * 1_000_000_000))


def stream_body(entries: list[tuple[str, dict, str]]) -> dict:
    """A Loki query_range response from (timestamp, labels, line) tuples."""
    streams: dict[str, dict] = {}
    for ts, labels, line in entries:
        key = json.dumps(labels, sort_keys=True)
        streams.setdefault(key, {"stream": labels, "values": []})
        streams[key]["values"].append([ts, line])
    return {"status": "success", "data": {"resultType": "streams", "result": list(streams.values())}}


class FakeLoki(Loki):
    """A Loki whose HTTP layer is canned, so the real parsing and query building stay in the path."""

    def __init__(self, ranges: dict | None = None, instants: dict | None = None,
                 labels: list[str] | None = None, values: dict | None = None) -> None:
        super().__init__(url="http://fake.loki")
        self.ranges = ranges or {}
        self.instants = instants or {}
        self._labels = labels or []
        self._values = values or {}
        self.queries: list[tuple[str, dict]] = []

    def _get(self, path: str, params: dict) -> dict:
        self.queries.append((path, params))
        if path == "/loki/api/v1/labels":
            return {"status": "success", "data": self._labels}
        if path.startswith("/loki/api/v1/label/"):
            name = path.rsplit("/", 2)[1]
            return {"status": "success", "data": self._values.get(name, [])}
        if path == "/loki/api/v1/query":
            return self.instants.get(params["query"], {"status": "success", "data": {"result": []}})
        if path == "/loki/api/v1/query_range":
            return self.ranges.get(params["query"], {"status": "success", "data": {"result": []}})
        raise LogKitError(f"unexpected path {path}")


class TestAuthentication(unittest.TestCase):
    """Loki has no auth of its own, so it is usually behind something that does."""

    def setUp(self) -> None:
        for name in ("LOKI_USERNAME", "LOKI_PASSWORD", "LOKI_TOKEN", "LOKI_URL"):
            os.environ.pop(name, None)

    def test_no_credentials_sends_no_authorization(self) -> None:
        self.assertNotIn("Authorization", Loki().headers())

    def test_basic_auth_for_an_access_list(self) -> None:
        # base64("cris:s3cret"), which is what Nginx Proxy Manager's Access List expects.
        header = Loki(username="cris", password="s3cret").headers()["Authorization"]
        self.assertEqual(header, "Basic Y3JpczpzM2NyZXQ=")

    def test_bearer_token(self) -> None:
        header = Loki(token="abc123").headers()["Authorization"]
        self.assertEqual(header, "Bearer abc123")

    def test_a_token_wins_over_a_username(self) -> None:
        header = Loki(username="cris", password="x", token="abc123").headers()["Authorization"]
        self.assertEqual(header, "Bearer abc123")

    def test_credentials_come_from_the_environment(self) -> None:
        os.environ["LOKI_USERNAME"] = "cris"
        os.environ["LOKI_PASSWORD"] = "s3cret"
        self.assertEqual(Loki().headers()["Authorization"], "Basic Y3JpczpzM2NyZXQ=")

    def test_an_explicit_argument_beats_the_environment(self) -> None:
        os.environ["LOKI_TOKEN"] = "from-env"
        self.assertEqual(Loki(token="explicit").headers()["Authorization"], "Bearer explicit")

    def test_a_password_is_not_required_with_a_username(self) -> None:
        # An empty password is a legitimate basic-auth credential; raising here would be a worse
        # failure than sending it and being told 401.
        self.assertTrue(Loki(username="cris").headers()["Authorization"].startswith("Basic "))


class TestParseWindow(unittest.TestCase):
    def test_durations(self) -> None:
        self.assertEqual(parse_window("30m"), timedelta(minutes=30))
        self.assertEqual(parse_window("2h"), timedelta(hours=2))
        self.assertEqual(parse_window("7d"), timedelta(days=7))
        self.assertEqual(parse_window("1w"), timedelta(weeks=1))
        self.assertEqual(parse_window(None), timedelta(hours=24))
        self.assertEqual(parse_window(None, "1h"), timedelta(hours=1))

    def test_iso_instant_means_since_then(self) -> None:
        span = parse_window("2020-01-01T00:00:00Z")
        self.assertGreater(span, timedelta(days=365))

    def test_a_future_instant_does_not_produce_a_negative_window(self) -> None:
        self.assertGreater(parse_window("2999-01-01T00:00:00Z"), timedelta(0))

    def test_rejects_nonsense(self) -> None:
        for bad in ("soon", "5 parsecs", "12", "-3h"):
            with self.assertRaises(LogKitError):
                parse_window(bad)


class TestStreamsToLines(unittest.TestCase):
    def test_flattens_sorts_and_limits(self) -> None:
        body = stream_body([
            (_ns(60), {"app": "b"}, "second"),
            (_ns(120), {"app": "a"}, "first"),
            (_ns(1), {"app": "c"}, "third"),
        ])
        lines = streams_to_lines(body, limit=10)
        self.assertEqual([entry["line"] for entry in lines], ["first", "second", "third"])
        self.assertEqual(lines[0]["labels"]["app"], "a")
        self.assertEqual(len(streams_to_lines(body, limit=2)), 2)

    def test_ignores_malformed_values(self) -> None:
        body = {"data": {"result": [
            {"stream": {"app": "a"}, "values": [["not-a-number", "x"], ["1", "y"], []]},
        ]}}
        self.assertEqual([entry["line"] for entry in streams_to_lines(body)], ["y"])

    def test_empty_result(self) -> None:
        self.assertEqual(streams_to_lines({"data": {"result": []}}), [])


class TestParsedLine(unittest.TestCase):
    def test_structured_line_is_parsed(self) -> None:
        entry = {
            "ts_ns": 1_700_000_000_000_000_000,
            "labels": {"container": "jobs-app-1"},
            "line": json.dumps({
                "ts": "2026-10-04T00:00:00.000Z", "level": "error", "app": "jobs-app",
                "svc": "scrape", "msg": "agent failed", "trace_id": "a" * 32,
                "run_id": 615, "company": "acme", "timeout": True, "custom": "kept",
            }),
        }
        got = parsed_line(entry)
        self.assertEqual(got["level"], "error")
        self.assertEqual(got["msg"], "agent failed")
        self.assertEqual(got["company"], "acme")
        self.assertEqual(got["container"], "jobs-app-1")
        # A field the engine does not know about is preserved rather than dropped.
        self.assertEqual(got["fields"]["custom"], "kept")
        self.assertNotIn("ts", got.get("fields", {}), "the line's own ts must not be duplicated")

    def test_non_json_line_is_kept_as_raw(self) -> None:
        got = parsed_line({"ts_ns": 1, "labels": {}, "line": "Traceback (most recent call last):"})
        self.assertIn("Traceback", got["raw"])
        self.assertNotIn("level", got)

    def test_json_that_is_not_an_object_is_raw(self) -> None:
        got = parsed_line({"ts_ns": 1, "labels": {}, "line": "[1, 2, 3]"})
        self.assertEqual(got["raw"], "[1, 2, 3]")

    def test_empty_values_are_omitted(self) -> None:
        got = parsed_line({"ts_ns": 1, "labels": {}, "line": json.dumps({"msg": "x", "run_id": 0, "company": ""})})
        self.assertNotIn("company", got)
        self.assertNotIn("run_id", got, "a zero id is omitted rather than reported as real")


class TestTraceTimeline(unittest.TestCase):
    def test_builds_a_timeline_across_services(self) -> None:
        trace = "b" * 32
        query = f'{{app=~".+"}} | trace_id = "{trace}"'
        body = stream_body([
            (_ns(30), {"app": "jobs-app", "svc": "batch"},
             json.dumps({"level": "info", "app": "jobs-app", "svc": "batch", "msg": "sweep started",
                         "trace_id": trace, "sweep_id": 457})),
            (_ns(20), {"app": "jobs-app", "svc": "scrape"},
             json.dumps({"level": "info", "app": "jobs-app", "svc": "scrape", "msg": "start", "span": "start",
                         "trace_id": trace, "run_id": 615, "company": "acme"})),
            (_ns(5), {"app": "jobs-app", "svc": "scrape"},
             json.dumps({"level": "error", "app": "jobs-app", "svc": "scrape", "msg": "agent failed",
                         "span": "agent", "trace_id": trace, "run_id": 615, "company": "acme"})),
        ])
        kit = LogKit(FakeLoki(ranges={query: body}))
        got = kit.trace_timeline(trace)

        self.assertEqual(got["lines"], 3)
        self.assertEqual(got["errors"], 1)
        self.assertEqual([line["msg"] for line in got["timeline"]],
                         ["sweep started", "start", "agent failed"])
        self.assertEqual(got["spans"], {"-": 1, "start": 1, "agent": 1})
        self.assertEqual(got["timeline"][-1]["company"], "acme")

    def test_rejects_a_malformed_trace_id(self) -> None:
        kit = LogKit(FakeLoki())
        for bad in ("", "abc", "Z" * 32, "a" * 31, "a" * 33):
            with self.assertRaises(LogKitError):
                kit.trace_timeline(bad)

    def test_normalises_case(self) -> None:
        trace = "c" * 32
        fake = FakeLoki(ranges={f'{{app=~".+"}} | trace_id = "{trace}"': stream_body([])})
        LogKit(fake).trace_timeline(trace.upper())
        self.assertIn(trace, fake.queries[0][1]["query"])


class TestErrorSummary(unittest.TestCase):
    def test_groups_and_sorts_counts(self) -> None:
        query = "sum by (app, svc, msg) (count_over_time({level=~\"error\"} | json [86400s]))"
        body = {"status": "success", "data": {"result": [
            {"metric": {"app": "jobs-app", "svc": "scrape", "msg": "agent failed"}, "value": [1, "16"]},
            {"metric": {"app": "apply-app", "svc": "worker", "msg": "postcondition_failed"}, "value": [1, "3"]},
        ]}}
        kit = LogKit(FakeLoki(instants={query: body}))
        got = kit.error_summary("24h")

        self.assertEqual(got["total"], 19)
        self.assertEqual(got["groups"], 2)
        self.assertEqual(got["errors"][0]["count"], 16)
        self.assertEqual(got["errors"][0]["msg"], "agent failed")
        self.assertEqual(got["errors"][1]["app"], "apply-app")

    def test_window_becomes_seconds_in_the_query(self) -> None:
        fake = FakeLoki()
        LogKit(fake).error_summary("2h")
        self.assertIn("[7200s]", fake.queries[0][1]["query"])

    def test_app_filter_narrows_the_selector(self) -> None:
        fake = FakeLoki()
        LogKit(fake).error_summary("1h", level="error|warn", app="apply-app")
        self.assertIn('level=~"error|warn"', fake.queries[0][1]["query"])
        self.assertIn('app="apply-app"', fake.queries[0][1]["query"])

    def test_an_unparsed_message_is_labelled(self) -> None:
        query = "sum by (app, svc, msg) (count_over_time({level=~\"error\"} | json [3600s]))"
        body = {"status": "success", "data": {"result": [
            {"metric": {"app": "x"}, "value": [1, "2"]},
        ]}}
        got = LogKit(FakeLoki(instants={query: body})).error_summary("1h")
        self.assertEqual(got["errors"][0]["msg"], "(unparsed)")


class TestContainerHealth(unittest.TestCase):
    def test_ranks_by_problem_density_not_volume(self) -> None:
        total = 'sum by (container) (count_over_time({container=~".+"} [86400s]))'
        problems = ('sum by (container) (count_over_time({container=~".+"} |~ '
                    '"(?i)panic|fatal|oomkill|out of memory|traceback|exit status|unhandled" [86400s]))')
        kit = LogKit(FakeLoki(instants={
            total: {"data": {"result": [
                {"metric": {"container": "noisy"}, "value": [1, "100000"]},
                {"metric": {"container": "broken"}, "value": [1, "100"]},
            ]}},
            problems: {"data": {"result": [
                {"metric": {"container": "noisy"}, "value": [1, "1"]},
                {"metric": {"container": "broken"}, "value": [1, "50"]},
            ]}},
        }))
        got = kit.container_health("24h")

        self.assertEqual(got["containers"][0]["container"], "broken", "density beats volume")
        self.assertEqual(got["containers"][0]["problem_per_1k"], 500.0)
        self.assertEqual(got["containers"][1]["problem_per_1k"], 0.0)

    def test_a_container_with_no_problems_reports_zero(self) -> None:
        total = 'sum by (container) (count_over_time({container=~".+"} [3600s]))'
        kit = LogKit(FakeLoki(instants={total: {"data": {"result": [
            {"metric": {"container": "quiet"}, "value": [1, "10"]},
        ]}}}))
        got = kit.container_health("1h")
        self.assertEqual(got["containers"][0]["problem_lines"], 0)


class TestStatus(unittest.TestCase):
    def test_reports_labels_and_values(self) -> None:
        kit = LogKit(FakeLoki(labels=["app", "svc", "level", "container"],
                              values={"app": ["jobs-app", "apply-app"], "svc": ["scrape"], "level": ["info"]}))
        got = kit.status("24h")
        self.assertIn("container", got["labels"])
        self.assertEqual(got["label_values"]["app"], ["apply-app", "jobs-app"], "values come back sorted")

    def test_counts_tell_you_what_is_actually_arriving(self) -> None:
        """The value list comes from the index and can name levels nothing is logging; the counts are
        the truth, and they are what stops an agent chasing an error that is not there."""
        fake = FakeLoki(
            labels=["level", "app", "svc"],
            values={"level": ["info", "error", "critical"], "app": ["jobs-app"], "svc": ["server"]},
            instants={
                'sum by (level) (count_over_time({level=~".+"} [86400s]))': {"data": {"result": [
                    {"metric": {"level": "info"}, "value": [1, "305"]},
                ]}},
                'sum by (app) (count_over_time({app=~".+"} [86400s]))': {"data": {"result": [
                    {"metric": {"app": "jobs-app"}, "value": [1, "272"]},
                ]}},
                'sum by (svc) (count_over_time({svc=~".+"} [86400s]))': {"data": {"result": [
                    {"metric": {"svc": "server"}, "value": [1, "272"]},
                ]}},
            },
        )
        got = LogKit(fake).status("24h")
        self.assertIn("error", got["label_values"]["level"], "the index still lists it")
        self.assertNotIn("error", got["counts_in_window"]["level"], "but nothing is logging it")
        self.assertEqual(got["counts_in_window"]["level"], {"info": 305})
        self.assertIn("from the index", got["note"])

    def test_nothing_arriving_is_called_out_as_a_collector_problem(self) -> None:
        fake = FakeLoki(
            labels=["level"],
            values={"level": ["info"]},
            instants={'sum by (level) (count_over_time({level=~".+"} [3600s]))':
                      {"data": {"result": []}}},
        )
        got = LogKit(fake).status("1h")
        self.assertEqual(got["counts_in_window"]["level"], {})
        self.assertIn("collector rather than at a quiet system", got["note"])

    def test_only_known_labels_are_interpolated_into_a_query(self) -> None:
        with self.assertRaises(LogKitError):
            LogKit(FakeLoki())._value_counts("level} | drop_all", timedelta(hours=1))

    def test_a_failing_store_is_reported_not_raised_blindly(self) -> None:
        class Broken(Loki):
            def _get(self, path: str, params: dict) -> dict:
                raise LogKitError("connection refused")

        with self.assertRaises(LogKitError):
            LogKit(Broken()).status()


class TestCodeFreshness(unittest.TestCase):
    def test_status_reports_whether_the_process_is_older_than_its_source(self) -> None:
        # Restarting the harness is the only way to load an edit, and a stale process is otherwise
        # indistinguishable from a current one: the tools answer either way.
        fake = FakeLoki(labels=["app"], values={"app": ["jobs-app"]})
        kit = LogKit(fake)
        got = kit.status("1h")
        self.assertIn("mcp", got)
        self.assertIn("stale", got["mcp"])
        self.assertIn("loaded_at", got["mcp"])
        self.assertFalse(got["mcp"]["stale"], "a process just started is not stale")

    def test_a_source_newer_than_the_import_is_flagged_stale(self) -> None:
        fake = FakeLoki()
        kit = LogKit(fake)
        # Pretend this module was imported a minute before the file was written.
        original = logkit._LOADED_AT
        try:
            logkit._LOADED_AT = original - 60
            got = kit._code_freshness()
            self.assertTrue(got["stale"])
            self.assertIn("restart", got["note"])
        finally:
            logkit._LOADED_AT = original


class TestSearchLogs(unittest.TestCase):
    def test_passes_the_query_through_and_parses_the_result(self) -> None:
        query = '{app="jobs-app"} | json | company="cisco"'
        body = stream_body([
            (_ns(10), {"app": "jobs-app"}, json.dumps({"level": "info", "msg": "summary", "company": "cisco"})),
        ])
        kit = LogKit(FakeLoki(ranges={query: body}))
        got = kit.search_logs(query, window="24h")
        self.assertEqual(got["returned"], 1)
        self.assertEqual(got["lines"][0]["company"], "cisco")


def live_loki_available() -> bool:
    """Whether a real Loki is reachable, so the integration class can skip instead of fail."""
    import urllib.request

    url = os.environ.get("LOKI_URL", "http://127.0.0.1:3100") + "/ready"
    try:
        with urllib.request.urlopen(url, timeout=3) as response:
            return response.status == 200
    except (urllib.error.URLError, TimeoutError, OSError):
        return False


# ------------------------------------------------------------------ lookups by key


def run_body(entries: list[tuple[float, dict]]) -> dict:
    """A query_range body from (seconds_ago, structured line) tuples.

    Application fields go at the top level of the JSON body, exactly as the apps log them; parsed_line
    is what moves the unrecognised ones under `fields`.
    """
    return stream_body([
        (_ns(age), {"app": "jobs-app", "container": "jobs_app"}, json.dumps(line))
        for age, line in entries
    ])


class TestLookupQueryBuilding(unittest.TestCase):
    def test_lookups_filter_structured_metadata_without_parsing(self) -> None:
        # These fields are structured metadata, so the store can filter them against the index. Doing it
        # with `| json` instead would parse every line in the window to find one run.
        for key, value in (("run_id", "630"), ("sweep_id", "629"), ("company", "acme"),
                           ("listing_id", "12"), ("trace_id", "a" * 32)):
            with self.subTest(key=key):
                fake = FakeLoki()
                LogKit(fake).search_logs("{app=~\".+\"}", window="1h")  # primes no state
                fake.queries.clear()
                if key == "trace_id":
                    LogKit(fake).trace_timeline(value, window="1h")
                else:
                    method = {"run_id": "run_timeline", "sweep_id": "sweep_timeline",
                              "company": "company_history", "listing_id": "listing_story"}[key]
                    argument = int(value) if key in ("run_id", "sweep_id", "listing_id") else value
                    getattr(LogKit(fake), method)(argument, window="1h")
                query = fake.queries[0][1]["query"]
                self.assertIn(f"| {key} = ", query)
                self.assertNotIn("| json", query)

    def test_a_quote_in_a_company_cannot_break_the_query(self) -> None:
        # An unescaped quote makes the query error, and an error is indistinguishable from "no such
        # company" at this end.
        fake = FakeLoki()
        LogKit(fake).company_history('acme" or x="', window="1h")
        query = fake.queries[0][1]["query"]
        self.assertIn(r'\"', query, "the quote must be escaped")
        self.assertNotIn('acme" or', query, "the raw quote must not reach the query")

    def test_rejects_ids_that_are_not_positive_integers(self) -> None:
        for bad in (0, -1, "630"):
            with self.subTest(bad=bad):
                with self.assertRaises(LogKitError):
                    LogKit(FakeLoki()).run_timeline(bad)  # type: ignore[arg-type]
        with self.assertRaises(LogKitError):
            LogKit(FakeLoki()).company_history("   ")


class TestRunFacts(unittest.TestCase):
    def facts(self, lines: list[dict]) -> dict:
        entries = [{"ts_ns": int(_ns(5)), "labels": {"app": "jobs-app"}, "line": json.dumps(l)}
                   for l in lines]
        return logkit._run_facts([logkit.parsed_line(e) for e in entries])

    def test_outcome_prefers_the_most_specific_evidence(self) -> None:
        cases = [
            ([{"msg": "agent failed", "error": "timeout"}, {"msg": "summary"}], "agent_failed"),
            ([{"msg": "listings page blocked", "block_reason": "captcha"}], "blocked"),
            ([{"msg": "skip", "reason": "robots_disallowed"}], "robots_disallowed"),
            ([{"msg": "skip"}], "skipped"),
            ([{"msg": "listing stored", "listing_id": 7}], "stored"),
            ([{"msg": "summary", "total_s": 3}], "completed"),
            ([{"msg": "start"}], "in_progress"),
        ]
        for lines, want in cases:
            with self.subTest(want=want):
                self.assertEqual(self.facts(lines)["outcome"], want)

    def test_counts_steps_and_falls_back_to_the_reported_number(self) -> None:
        seen = self.facts([{"msg": "agent step", "step": 1}, {"msg": "agent step", "step": 2}])
        self.assertEqual(seen["agent_steps"], 2)
        reported = self.facts([{"msg": "agent failed", "agent_steps": 9}])
        self.assertEqual(reported["agent_steps"], 9)
        self.assertIsNone(self.facts([{"msg": "start"}])["agent_steps"])

    def test_collects_listing_ids_and_flags_a_block(self) -> None:
        got = self.facts([
            {"msg": "listing stored", "listing_id": 11},
            {"msg": "listing stored", "listing_id": 12},
            {"msg": "summary", "blocked": True, "block_reason": "challenge"},
        ])
        self.assertEqual(got["listing_ids"], [11, 12])
        self.assertEqual(got["listings_stored"], 2)
        self.assertEqual(got["outcome"], "blocked")
        self.assertEqual(got["block_reason"], "challenge")


class TestRunTimeline(unittest.TestCase):
    def test_summarises_a_run_from_its_lines(self) -> None:
        query = '{app=~".+"} | run_id = "630"'
        body = run_body([
            (60, {"msg": "start", "run_id": 630, "company": "acme", "vendor": "workday"}),
            (50, {"msg": "agent step", "run_id": 630, "company": "acme", "step": 1}),
            (40, {"msg": "agent step", "run_id": 630, "company": "acme", "step": 2}),
            (20, {"msg": "agent decided", "run_id": 630, "company": "acme",
                "listings_url": "https://x.test/jobs", "remote_confirmed": False,
                "agent_steps": 2, "agent_s": 41.5}),
            (10, {"msg": "summary", "run_id": 630, "company": "acme",
                "agent_s": 41.5, "fetch_s": 3.2, "store_s": 0.1, "total_s": 45.0,
                "inserted": 4, "quality": ""}),
        ])
        got = LogKit(FakeLoki(ranges={query: body})).run_timeline(630, window="1h")
        self.assertTrue(got["found"])
        self.assertEqual(got["company"], "acme")
        self.assertEqual(got["vendor"], "workday")
        self.assertEqual(got["agent_steps"], 2)
        self.assertEqual(got["agent_s"], 41.5)
        self.assertEqual(got["listings_url"], "https://x.test/jobs")
        self.assertEqual(got["postings"]["inserted"], 4)
        self.assertEqual(got["agent"]["remote_confirmed"], False)
        self.assertIsNone(got["quality"], "an empty quality must read as absent, not as a value")

    def test_an_unknown_run_says_so_rather_than_returning_nothing(self) -> None:
        got = LogKit(FakeLoki()).run_timeline(999999, window="1h")
        self.assertFalse(got["found"])
        self.assertEqual(got["lines"], 0)
        self.assertIn("window", got.get("note", ""))


class TestSweepTimeline(unittest.TestCase):
    def test_groups_companies_by_run_and_tallies_outcomes(self) -> None:
        query = '{app=~".+"} | sweep_id = "629"'
        body = run_body([
            (90, {"msg": "sweep started", "sweep_id": 629,
                "companies": 2, "candidates": 436, "limit": 2, "skip_ok": True, "skip_traced": False}),
            (80, {"msg": "company skipped", "sweep_id": 629, "company": "skipped-co",
                  "reason": "last run ok"}),
            (70, {"msg": "company started", "sweep_id": 629, "company": "a-co", "index": 1}),
            (60, {"msg": "start", "sweep_id": 629, "run_id": 700, "company": "a-co",
                  "vendor": "lever"}),
            (50, {"msg": "skip", "sweep_id": 629, "run_id": 700, "company": "a-co",
                  "reason": "no_remote_roles"}),
            (40, {"msg": "company started", "sweep_id": 629, "company": "b-co", "index": 2}),
            (30, {"msg": "start", "sweep_id": 629, "run_id": 701, "company": "b-co",
                  "vendor": "workday"}),
            (20, {"msg": "listing stored", "sweep_id": 629, "run_id": 701, "company": "b-co",
                  "listing_id": 5}),
            (10, {"msg": "company finished", "sweep_id": 629, "company": "b-co",
                  "duration_ms": 1234}),
        ])
        got = LogKit(FakeLoki(ranges={query: body})).sweep_timeline(629, window="1h")
        self.assertEqual(got["plan"]["companies"], 2)
        self.assertEqual(got["plan"]["limit"], 2)
        self.assertEqual(got["skipped_before_starting"],
                         [{"company": "skipped-co", "reason": "last run ok"}])
        self.assertEqual(got["outcomes"], {"no_remote_roles": 1, "stored": 1})
        by_run = {row["run_id"]: row for row in got["companies"]}
        self.assertEqual(by_run[700]["company"], "a-co")
        self.assertEqual(by_run[700]["outcome"], "no_remote_roles")
        self.assertEqual(by_run[701]["outcome"], "stored")
        self.assertEqual(by_run[701]["listings_stored"], 1)
        self.assertEqual(by_run[701]["duration_ms"], 1234)


class TestCompanyHistory(unittest.TestCase):
    def test_lists_runs_newest_first_with_a_tally(self) -> None:
        query = '{app=~".+"} | company = "abbott-laboratories"'
        body = run_body([
            (500, {"msg": "start", "run_id": 10, "company": "abbott-laboratories",
                   "vendor": "workday"}),
            (400, {"msg": "agent failed", "run_id": 10, "company": "abbott-laboratories",
                   "error": "timed out", "agent_steps": 12}),
            (90, {"msg": "start", "run_id": 20, "company": "abbott-laboratories",
                  "vendor": "workday"}),
            (80, {"msg": "agent failed", "run_id": 20, "company": "abbott-laboratories",
                  "error": "timed out", "agent_steps": 14}),
        ])
        got = LogKit(FakeLoki(ranges={query: body})).company_history("abbott-laboratories", window="30d")
        self.assertEqual(got["runs"], 2)
        self.assertEqual(got["outcomes"], {"agent_failed": 2})
        self.assertEqual([row["run_id"] for row in got["history"]], [20, 10],
                         "newest run first")
        self.assertEqual(got["history"][0]["agent_steps"], 14)


class TestCompanyHistorySkips(unittest.TestCase):
    def test_a_company_that_was_only_ever_skipped_is_not_empty_history(self) -> None:
        # Found by using the tool: a company the batch skipped has no run_id, so grouping by run alone
        # reported zero history for a company the sweep had considered and passed over on many sweeps.
        query = '{app=~".+"} | company = "abbott-laboratories"'
        body = run_body([
            (600, {"msg": "company skipped", "company": "abbott-laboratories", "sweep_id": 620,
                   "reason": "browser-use trace present"}),
            (60, {"msg": "company skipped", "company": "abbott-laboratories", "sweep_id": 632,
                  "reason": "last run ok"}),
        ])
        got = LogKit(FakeLoki(ranges={query: body})).company_history("abbott-laboratories", window="30d")
        self.assertEqual(got["runs"], 0)
        self.assertEqual(got["skipped"], 2)
        self.assertEqual(got["outcomes"], {"skipped_before_running": 2})
        self.assertEqual([row["reason"] for row in got["history"]],
                         ["last run ok", "browser-use trace present"], "newest first")


class TestListingStory(unittest.TestCase):
    def test_separates_discovery_from_applications(self) -> None:
        query = '{app=~".+"} | listing_id = "12345"'
        body = stream_body([
            (_ns(500), {"app": "jobs-app", "container": "jobs_app"}, json.dumps({
                "msg": "listing stored", "listing_id": 12345, "company": "acme", "run_id": 700,
                "is_remote": True, "title": "Staff Engineer", "url": "https://x.test/1"})),
            (_ns(60), {"app": "apply-app", "container": "apply_app"}, json.dumps({
                "app": "apply-app", "msg": "application submitted", "listing_id": 12345,
                "application_id": 9})),
        ])
        got = LogKit(FakeLoki(ranges={query: body})).listing_story(12345, window="30d")
        self.assertEqual(got["company"], "acme")
        self.assertEqual(got["title"], "Staff Engineer")
        self.assertTrue(got["is_remote"])
        self.assertEqual(len(got["discovered"]), 1)
        self.assertEqual(got["discovered"][0]["run_id"], 700)
        self.assertEqual(len(got["applications"]), 1)
        self.assertEqual(got["applications"][0]["app"], "apply-app")

    def test_a_listing_nothing_references_yet_explains_the_contract(self) -> None:
        got = LogKit(FakeLoki()).listing_story(1, window="30d")
        self.assertFalse(got["found"])
        self.assertIn("listing_id", got["note"])


@unittest.skipUnless(os.environ.get("LOKI_URL_TEST") or live_loki_available(), "no Loki reachable")
class TestAgainstRealLoki(unittest.TestCase):
    """The LogQL these tools build, checked against a real Loki: a fake proves the code does what this
    file assumes, and only a real store proves the assumption was right."""

    def test_status_query_parses(self) -> None:
        got = LogKit().status("1h")
        self.assertIn("labels", got)

    def test_error_summary_query_parses(self) -> None:
        # The aggregation is the risky part of the generated LogQL: Loki rejects an unknown grouping
        # label outright, so this fails loudly if the shape is wrong.
        got = LogKit().error_summary("1h")
        self.assertIn("total", got)

    def test_container_health_query_parses(self) -> None:
        got = LogKit().container_health("1h")
        self.assertIn("containers", got)

    def test_trace_timeline_round_trips_through_the_store(self) -> None:
        """Push one trace across two services and read it back in order.

        This verifies the correlation design end to end. The fakes in the rest of the suite prove the
        parsing is right; only a real store proves the query - `| trace_id = "..."` against
        structured metadata - is one Loki actually accepts.
        """
        import random
        import time
        import urllib.request

        trace = "%032x" % random.getrandbits(128)
        now = int(time.time() * 1e9)
        streams = []
        entries = [
            ("jobs-app", "server", "info", "request"),
            ("jobs-app", "scrape", "error", "agent failed"),
        ]
        for offset, (app, svc, level, msg) in enumerate(entries):
            body = json.dumps({"level": level, "msg": msg, "app": app, "svc": svc, "trace_id": trace})
            streams.append({
                # Timestamps must be in the past: Loki drops entries too far in the future, silently,
                # which is exactly the trap this test was written after falling into.
                "stream": {"app": app, "svc": svc, "level": level, "container": f"{app}-test"},
                "values": [[str(now - (10 - offset * 5) * 10**9), body, {"trace_id": trace}]],
            })
        # Pushed through the client rather than a bare urlopen, so the test exercises the same
        # authenticated path the tools read with: against a Loki behind an Nginx Proxy Manager Access
        # List, a test that bypassed the credentials would pass while the tools failed.
        self.assertIn(Loki().push(streams), (200, 204))

        time.sleep(2)  # let the ingester make it queryable
        got = LogKit().trace_timeline(trace, window="5m")
        self.assertEqual(got["lines"], 2, got)
        self.assertEqual([line["msg"] for line in got["timeline"]], ["request", "agent failed"])
        self.assertEqual([line["svc"] for line in got["timeline"]], ["server", "scrape"])
        self.assertEqual(got["errors"], 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
