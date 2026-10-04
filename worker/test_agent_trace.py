# test_agent_trace.py: the contract between the two places a trace goes.
#
# The trace file keeps everything; stderr gets a compact line. That split is the whole point - the log
# store should learn the *shape* of a run (progress, churn, how long) without receiving a language
# model's reasoning on every step - so the assertions that matter here are about what is NOT sent.
from __future__ import annotations

import io
import json
import os
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from agent_trace import TraceWriter, correlation, step_line  # noqa: E402


class FakeAction:
    def __init__(self, **kwargs):
        self._data = kwargs

    def model_dump(self, exclude_none=True):
        return self._data


class FakeState:
    url = "https://jobs.acme.test/careers?q=engineer"


class FakeOutput:
    def __init__(self, actions):
        self.thinking = "a great deal of model reasoning " * 40
        self.evaluation_previous_goal = "the previous goal was evaluated at length " * 10
        self.memory = "everything remembered so far " * 40
        self.next_goal = "Click the Remote filter"
        self.action = actions


class WriterTestCase(unittest.TestCase):
    def setUp(self) -> None:
        self.dir = tempfile.mkdtemp(prefix="agent-trace-")
        self.addCleanup(shutil.rmtree, self.dir, ignore_errors=True)
        self.path = os.path.join(self.dir, "trace.jsonl")
        # Redirect stderr so a test can read the compact lines; the writer holds no reference to it.
        self.stderr = io.StringIO()
        original = sys.stderr
        sys.stderr = self.stderr
        self.addCleanup(lambda: setattr(sys, "stderr", original))
        for name in ("JOBS_TRACE_ID", "JOBS_SWEEP_ID", "JOBS_RUN_ID", "JOBS_COMPANY", "JOBS_SERVICE", "JOBS_APP"):
            os.environ.pop(name, None)

    def file_events(self, path: str) -> list[dict]:
        with open(path, encoding="utf-8") as handle:
            return [json.loads(line) for line in handle if line.strip()]

    def stderr_lines(self) -> list[dict]:
        return [json.loads(line) for line in self.stderr.getvalue().splitlines() if line.strip()]


class TestCorrelation(WriterTestCase):
    def test_defaults_without_an_environment(self) -> None:
        got = correlation()
        self.assertEqual(got["app"], "jobs-app")
        self.assertEqual(got["svc"], "agent")
        self.assertNotIn("trace_id", got)

    def test_reads_the_environment_the_parent_set(self) -> None:
        os.environ["JOBS_TRACE_ID"] = "a" * 32
        os.environ["JOBS_SWEEP_ID"] = "620"
        os.environ["JOBS_RUN_ID"] = "621"
        os.environ["JOBS_COMPANY"] = "abm-industries-inc"
        os.environ["JOBS_SERVICE"] = "scrape"

        got = correlation()
        self.assertEqual(got["trace_id"], "a" * 32)
        self.assertEqual(got["company"], "abm-industries-inc")
        self.assertEqual(got["svc"], "scrape")
        # Ids are numbers so `| run_id = 621` compares like-for-like with the Go side.
        self.assertEqual(got["sweep_id"], 620)
        self.assertEqual(got["run_id"], 621)

    def test_a_non_numeric_id_is_kept_as_text_rather_than_lost(self) -> None:
        os.environ["JOBS_RUN_ID"] = "not-a-number"
        self.assertEqual(correlation()["run_id"], "not-a-number")


class TestStepLine(WriterTestCase):
    def test_carries_the_shape_of_the_step(self) -> None:
        os.environ["JOBS_TRACE_ID"] = "b" * 32
        os.environ["JOBS_COMPANY"] = "acme"
        line = step_line({
            "event": "step", "step": 3, "url": "https://x.test/a",
            "actions": [{"click": {"index": 26}}, {"scroll": {"down": True}}],
        })
        self.assertEqual(line["msg"], "agent step")
        self.assertEqual(line["span"], "agent")
        self.assertEqual(line["step"], 3)
        self.assertEqual(line["url"], "https://x.test/a")
        self.assertEqual(line["actions"], ["click", "scroll"])
        self.assertEqual(line["action_count"], 2)
        self.assertEqual(line["company"], "acme")

    def test_sends_no_model_text(self) -> None:
        """The assertion this file exists for. The log store gets the journey, not the reasoning."""
        line = step_line({
            "event": "step", "step": 1, "url": "https://x.test",
            "thinking": "reasoning " * 100,
            "memory": "memory " * 100,
            "evaluation_previous_goal": "evaluation " * 100,
            "next_goal": "Click the filter",
            "actions": [{"click": {}}],
        })
        for forbidden in ("thinking", "memory", "evaluation_previous_goal", "next_goal"):
            self.assertNotIn(forbidden, line, f"{forbidden} must not go to the log store")
        # And nothing large sneaked in under another name.
        self.assertLess(len(json.dumps(line)), 500)

    def test_omits_what_is_absent_rather_than_writing_nulls(self) -> None:
        line = step_line({"event": "step", "step": 1})
        self.assertNotIn("url", line)
        self.assertNotIn("actions", line)
        self.assertNotIn("action_count", line)


class TestWriterEmitsBothDestinations(WriterTestCase):
    def test_a_step_writes_the_full_event_to_the_file_and_the_shape_to_stderr(self) -> None:
        writer = TraceWriter(self.path)
        writer.step_callback()(FakeState(), FakeOutput([FakeAction(click={"index": 26})]), 1)

        events = self.file_events(self.path)
        self.assertEqual(len(events), 1)
        # The file keeps the reasoning: it is the evidence, and the traces MCP reads it.
        self.assertIn("thinking", events[0])
        self.assertIn("next_goal", events[0])
        self.assertEqual(events[0]["step"], 1)

        lines = self.stderr_lines()
        self.assertEqual(len(lines), 1)
        self.assertEqual(lines[0]["msg"], "agent step")
        self.assertEqual(lines[0]["actions"], ["click"])
        self.assertNotIn("thinking", lines[0])

    def test_completion_closes_the_span(self) -> None:
        class FakeHistory:
            def final_result(self):
                return "done"

            def is_successful(self):
                return True

            def number_of_steps(self):
                return 6

        writer = TraceWriter(self.path)
        writer.done_callback()(FakeHistory())

        kinds = [json.loads(line)["msg"] for line in self.stderr.getvalue().splitlines() if line.strip()]
        self.assertIn("agent finished", kinds)
        finished = next(
            json.loads(line) for line in self.stderr.getvalue().splitlines()
            if line.strip() and json.loads(line)["msg"] == "agent finished"
        )
        self.assertEqual(finished["agent_steps"], 6)
        self.assertTrue(finished["success"])

    def test_a_broken_stderr_never_breaks_the_run(self) -> None:
        class Exploding(io.StringIO):
            def write(self, *args, **kwargs):
                raise OSError("the pipe is gone")

            def flush(self):
                raise OSError("the pipe is gone")

        sys.stderr = Exploding()
        writer = TraceWriter(self.path)
        # Must not raise: these are progress lines, and a run may not fail because a log write did.
        writer.step_callback()(FakeState(), FakeOutput([FakeAction(click={})]), 1)
        self.assertEqual(len(self.file_events(self.path)), 1, "the trace file still got the event")

    def test_an_unwritable_trace_file_still_reports_progress(self) -> None:
        # A trace path the process cannot create leaves the file unwritable, but the log line is the
        # operator's only sign of life - so it must still be sent.
        writer = TraceWriter("/proc/definitely/not/writable/trace.jsonl")
        writer.step_callback()(FakeState(), FakeOutput([FakeAction(click={})]), 1)
        self.assertEqual(len(self.stderr_lines()), 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
