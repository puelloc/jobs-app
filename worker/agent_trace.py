# agent_trace.py: live agent-trace emission shared by the worker and the probe.
#
# One JSON object per event, appended to a caller-supplied JSONL file and flushed after each write so
# a poller (GET /api/traces/{id}) sees progress. Tracing is best-effort: a trace that cannot be
# written never breaks the run, and the one-process-per-request model means the OS closes the file
# when the process exits.
from __future__ import annotations

import json
import os
from datetime import datetime, timezone
from typing import Any, Callable


def _utc_now() -> str:
    """RFC3339 UTC with milliseconds, matching the timestamps the rest of the schema stores."""
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


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
            self.emit({
                "event": "step",
                "step": step,
                "url": getattr(state, "url", ""),
                "thinking": getattr(output, "thinking", None),
                "evaluation_previous_goal": getattr(output, "evaluation_previous_goal", None),
                "memory": getattr(output, "memory", None),
                "next_goal": getattr(output, "next_goal", None),
                "actions": [_action_summary(a) for a in actions],
            })

        return on_step

    def done_callback(self) -> Callable:
        """A register_done_callback closure: one `done` event at the end of the run."""

        def on_done(history) -> None:
            final = ""
            try:
                final = str(history.final_result() or "")
            except Exception:  # noqa: BLE001
                pass
            self.emit({
                "event": "done",
                "success": bool(history.is_successful()),
                "steps": int(history.number_of_steps()),
                "final_result": final,
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
