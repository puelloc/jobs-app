# agent_trace.py: live agent-trace emission shared by the worker and the probe.
#
# One JSON object per event, appended to a caller-supplied JSONL file and flushed after each write so
# a poller (GET /api/traces/{id}) sees progress. Tracing is best-effort: a trace that cannot be
# written never breaks the run, and the one-process-per-request model means the OS closes the file
# when the process exits.
from __future__ import annotations

import json
import os
import sys
from datetime import datetime, timezone
from typing import Any, Callable

# The correlation the parent process passes down. Read from the environment rather than plumbed through
# every call site, because the writer is a leaf used by several workers.
ENV_FIELDS = (
    ("JOBS_APP", "app"),
    ("JOBS_SERVICE", "svc"),
    ("JOBS_TRACE_ID", "trace_id"),
    ("JOBS_SWEEP_ID", "sweep_id"),
    ("JOBS_RUN_ID", "run_id"),
    ("JOBS_COMPANY", "company"),
)


def _utc_now() -> str:
    """RFC3339 UTC with milliseconds, matching the timestamps the rest of the schema stores."""
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def _as_number(value: str) -> Any:
    """Ids go on the wire as numbers, so `| run_id = 615` compares like-for-like with the Go side."""
    try:
        return int(value)
    except (TypeError, ValueError):
        return value


def correlation() -> dict[str, Any]:
    """The correlation fields for a stderr line, taken from the environment the parent set."""
    fields: dict[str, Any] = {"app": "jobs-app", "svc": "agent"}
    for env_name, key in ENV_FIELDS:
        raw = os.environ.get(env_name)
        if not raw:
            continue
        fields[key] = _as_number(raw) if key in ("sweep_id", "run_id") else raw
    return fields


def _emit_stderr(line: dict[str, Any]) -> None:
    """Write one compact JSON line to stderr, where the container's log collector reads it.

    Deliberately silent on failure. These lines are progress reporting, and a run must never fail
    because a log write did.
    """
    try:
        sys.stderr.write(json.dumps(line, ensure_ascii=False, default=str) + "\n")
        sys.stderr.flush()
    except Exception:  # noqa: BLE001 - telemetry must never break the run
        pass


def step_line(event: dict[str, Any]) -> dict[str, Any]:
    """A compact, loggable summary of one agent step.

    Action *names* only, and no model text: the full event - thinking, memory, evaluation - stays in
    the trace file, which is the right place for it. What goes to the log store is the shape of the
    journey, so progress is visible while a run is happening and churn is countable afterwards without
    shipping a language model's reasoning into a log database on every step.
    """
    line: dict[str, Any] = {"ts": _utc_now(), "level": "info", "msg": "agent step", "span": "agent"}
    line.update(correlation())
    if event.get("step") is not None:
        line["step"] = event["step"]
    if event.get("url"):
        line["url"] = event["url"]
    actions = [
        next(iter(action)) for action in (event.get("actions") or [])
        if isinstance(action, dict) and action
    ]
    if actions:
        line["actions"] = actions
        line["action_count"] = len(actions)
    return line


class TraceWriter:
    """Appends one JSON object per agent event to a file."""

    def __init__(self, path: str) -> None:
        self._fh = None
        if path:
            try:
                parent = os.path.dirname(path)
                if parent:
                    # The container's /data/traces may not exist on a fresh deploy; create it so the
                    # trace is not silently dropped.
                    os.makedirs(parent, exist_ok=True)
                self._fh = open(path, "a", encoding="utf-8")
            except OSError:
                self._fh = None

    def emit(self, event: dict[str, Any]) -> None:
        if self._fh is None:
            return
        # Every event is stamped. A trace without timing says what the agent did but not where the run
        # went, which is the first question when a 12-minute run only managed six steps. The field is
        # additive: readers that predate it ignore it, and older traces simply carry no timing.
        event = {**event, "ts": _utc_now()}
        try:
            self._fh.write(json.dumps(event, ensure_ascii=False, default=str) + "\n")
            self._fh.flush()
        except OSError:
            pass  # tracing must never break the run

    def step_callback(self) -> Callable:
        """A register_new_step_callback closure: one `step` event per agent step."""

        def on_step(state, output, step: int) -> None:
            actions = getattr(output, "action", None) or []
            if not isinstance(actions, (list, tuple)):
                actions = [actions]
            event = {
                "event": "step",
                "step": step,
                "url": getattr(state, "url", ""),
                "thinking": getattr(output, "thinking", None),
                "evaluation_previous_goal": getattr(output, "evaluation_previous_goal", None),
                "memory": getattr(output, "memory", None),
                "next_goal": getattr(output, "next_goal", None),
                "actions": [_action_summary(a) for a in actions],
            }
            self.emit(event)
            # The same step, compactly, to the log store. Without this a scrape's agent phase is a
            # single "start" line followed by minutes of silence, so progress and churn - how many
            # steps, on which pages, doing what - are invisible until the run is over.
            _emit_stderr(step_line(event))

        return on_step

    def done_callback(self) -> Callable:
        """A register_done_callback closure: one `done` event at the end of the run."""

        def on_done(history) -> None:
            final = ""
            try:
                final = str(history.final_result() or "")
            except Exception:  # noqa: BLE001
                pass
            steps = int(history.number_of_steps())
            self.emit({
                "event": "done",
                "success": bool(history.is_successful()),
                "steps": steps,
                "final_result": final,
            })
            # Completion is worth a line of its own: it closes the span opened by "start", so the store
            # can say how long an agent phase took and how many steps it needed.
            _emit_stderr({
                "ts": _utc_now(), "level": "info", "msg": "agent finished", "span": "agent",
                **correlation(), "success": bool(history.is_successful()), "agent_steps": steps,
            })

        return on_done


def _action_summary(action) -> dict:
    try:
        d = action.model_dump(exclude_none=True)
    except Exception:  # noqa: BLE001
        d = {"raw": str(action)}
    if isinstance(d, dict):
        d.pop("interacted_element", None)
    return d
