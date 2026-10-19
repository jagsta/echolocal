"""Transport-failure propagation tests (child 2 of card A restructure).

Regression for the live 15 Oct break point (child-1 diagnosis, t_01900f8e):
`_dot_action_async` swallowed the esphome transport error fire-and-forget
(blocking=False), so a dead HA<->dot native-API link left the session
RINGING (callee=ringing, caller=dialing) until the 30s gc ring timeout —
every retry in the window busy-rejected. Fix: surface the error
(blocking=True) and immediately tear down a still-ringing session whose
dial transport failed, freeing the caller to dial again.
Run with system python: /usr/bin/python3 -m pytest (hil-framework D-DI-7).
"""
from __future__ import annotations

import asyncio
from typing import Any

from custom_components.echolocal_dropin.const import RING_FLOOR_SECONDS
from custom_components.echolocal_dropin.device_map import dot_for, session_sensor_for
from custom_components.echolocal_dropin.runtime import DropInRuntime
from custom_components.echolocal_dropin.state_machine import (
    CallError,
    Endpoint,
    PermissionMap,
    CallStateMachine,
)


LOUNGE = "17b8d542d93b7f00a01a79c7e9edcf60"
KIT = "acf8f22077b452856f9ef46024f95d57"


class FakeHass:
    """Minimal hass stand-in: add_job + async services.async_call capture."""

    def __init__(self):
        self.states = type("S", (), {"get": staticmethod(lambda eid: None)})()
        self.jobs: list[tuple] = []
        self.service_calls: list[tuple] = []
        self.services = self

    def add_job(self, *args):
        self.jobs.append(args)

    async def async_call(self, domain: str, service: str, args: dict,
                         blocking: bool = True, **kw) -> None:
        raise NotImplementedError("override self.dial Behavior per test")

    async def async_add_executor_job(self, fn, *args):
        return fn(*args)


def make_runtime(dial_behavior) -> tuple[DropInRuntime, FakeHass]:
    """Build a runtime over a fake hass; dial_behavior(service) -> None|raise.

    A behavior returning None or a falsy value is treated as the dot's real
    answer {"state": "ringing"} (all four call-lane actions answer dot-side);
    a test wanting an EMPTY answer returns {} explicitly."""
    hass = FakeHass()

    async def fake_call(domain, service, args, blocking=True, **kw):
        hass.service_calls.append((domain, service, dict(args), blocking, kw))
        res = dial_behavior(service, dict(args))
        # None = no explicit answer scripted -> the dot's real answer;
        # {} stays {} (it IS the undelivered/empty case under test).
        return res if isinstance(res, dict) else {"state": "ringing"}

    hass.async_call = fake_call  # type: ignore[method-assign]

    rt = DropInRuntime.__new__(DropInRuntime)
    rt.hass = hass  # type: ignore[assignment]
    rt.entry = None
    rt.permissions = PermissionMap()
    rt.machine = CallStateMachine(permissions=rt.permissions, emit=rt._on_event)
    rt.relay = None
    rt._relays = {}
    rt._media_targets = {}
    rt._paused_for_call = set()
    rt.device_available = None
    return rt, hass


def drain(hass: FakeHass) -> None:
    """Execute pending add_job callbacks (coroutine fns) to completion.
    t_903017d5: the announce-then-dial job appends new jobs as it runs, so
    loop until the queue drains (the real event loop's schedule semantics)."""
    loop = asyncio.new_event_loop()
    try:
        while hass.jobs:
            jobs, hass.jobs = hass.jobs, []
            for fn, *argv in jobs:
                coro = fn(*argv)
                if asyncio.iscoroutine(coro):
                    loop.run_until_complete(coro)
                else:
                    fn(*argv)
    finally:
        loop.close()


def dial_fails_on(service: str, args: dict) -> None:
    if service.endswith("_dial"):
        raise ConnectionError("Not connected to kitchen @ 192.168.10.99!")


def test_failed_dial_transport_tears_down_session():
    """APIConnectionError on dropin_dial must end the ringing session NOW."""
    rt, hass = make_runtime(dial_fails_on)

    rt.machine.call("di-x", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    assert "di-x" in rt.machine.sessions
    assert rt.machine.endpoint_state(KIT) == "ringing"
    assert rt.machine.endpoint_state(LOUNGE) == "dialing"

    # Drain the fire-and-forget job: the dial transport error surfaces and
    # the runtime must tear the session down synchronously after it.
    drain(hass)

    assert "di-x" not in rt.machine.sessions, "failed ring must be reaped immediately"
    assert rt.machine.endpoint_state(LOUNGE) == "idle", "caller freed to dial again"
    assert rt.machine.endpoint_state(KIT) == "idle"
    # Graceful teardown: the end action was queued for both dots (drain the
    # teardown-emitted jobs and check).
    drain(hass)
    drain(hass)
    assert any(s.endswith("_end") for _, s, _, _, _ in hass.service_calls)


def test_successful_dial_keeps_session_ringing():
    rt, hass = make_runtime(lambda service, args: None)

    rt.machine.call("di-ok", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    drain(hass)
    assert "di-ok" in rt.machine.sessions
    assert rt.machine.endpoint_state(LOUNGE) == "dialing"
    assert rt.machine.endpoint_state(KIT) == "ringing"


def test_failed_end_transport_does_not_kill_active_session():
    """Only the DIAL failure path tears down; end failures just log."""
    rt, hass = make_runtime(
        lambda service, args: (
            None if service.endswith("_dial")
            else (_ for _ in ()).throw(ConnectionError("transport died"))
        )
    )

    t = [10.0]
    rt.machine.now = lambda: t[0]
    rt.machine.call("di-live", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    t[0] += RING_FLOOR_SECONDS + 1
    rt.machine.accept("di-live", KIT, at=t[0])
    assert rt.machine.endpoint_state(KIT) == "in_call"
    # teardown transport fails (hangup path) — session still ends machine-side
    rt.machine.hangup("di-live")
    assert "di-live" not in rt.machine.sessions
    drain(hass)  # the failing end transport is logged, not fatal


def test_teardown_of_failed_ring_frees_immediate_retry():
    """Caller can dial again right after a failed-ring teardown (no busy)."""
    rt, hass = make_runtime(dial_fails_on)

    rt.machine.call("di-a", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    drain(hass)  # di-a torn down
    assert "di-a" not in rt.machine.sessions
    # immediate retry, seconds later, must NOT busy-reject
    rt.machine.call("di-b", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    assert "di-b" in rt.machine.sessions
    drain(hass)
    assert "di-b" not in rt.machine.sessions


def test_teardown_skips_when_session_already_answered():
    """Race guard: if the callee answered before the error drained, leave it."""
    rt, hass = make_runtime(
        lambda service, args: (
            (_ for _ in ()).throw(ConnectionError("late transport error"))
            if service.endswith("_dial") else None
        )
    )

    t = [0.0]
    rt.machine.now = lambda: t[0]
    rt.machine.call("di-race", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    t[0] += RING_FLOOR_SECONDS + 1
    rt.machine.accept("di-race", KIT, at=t[0])  # answered before drain
    drain(hass)  # the late error must be a no-op
    assert "di-race" in rt.machine.sessions
    assert rt.machine.endpoint_state(KIT) == "in_call"
    rt.machine.hangup("di-race")  # clean up


def test_add_job_sync_error_also_tears_down():
    """The sync add_job failure path (hass gone) also reaps the ring."""
    rt, hass = make_runtime(dial_fails_on)

    def broken_add_job(*args):
        raise RuntimeError("loop closed")

    hass.add_job = broken_add_job  # type: ignore[assignment]
    rt.machine.call("di-sync", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    assert "di-sync" not in rt.machine.sessions
    assert rt.machine.endpoint_state(LOUNGE) == "idle"


# -- t_5c0b6869: missing/empty action response = UNDELIVERED ------------------


class _States:
    """hass.states stand-in with a per-entity state map."""

    def __init__(self, states: dict[str, str]):
        self._states = states

    def get(self, entity_id: str):
        value = self._states.get(entity_id)
        if value is None:
            return None
        return type("S", (), {"state": value})()


def make_runtime_with_states(dial_behavior, states: dict[str, str]):
    rt, hass = make_runtime(dial_behavior)
    hass.states = _States(states)  # type: ignore[method-assign]
    return rt, hass


def test_call_lane_requests_return_response():
    """Every call-lane action must ask for the dot's answer: with HA
    discarding ResponseData a vanished action was indistinguishable from a
    delivered one (the 30s 'Timeout waiting for response' surfaced long
    after the fact)."""
    import asyncio as _asyncio

    hass = FakeHass()
    seen: dict[str, Any] = {}

    async def spy(domain, service, data, blocking=True, **kw):
        seen.update(domain=domain, service=service, blocking=blocking,
                    return_response=kw.get("return_response"))
        return {"state": "ringing"}

    hass.async_call = spy  # type: ignore[method-assign]
    rt, _ = make_runtime(seen.setdefault)  # unused behavior; spy handles calls
    rt.hass = hass  # type: ignore[assignment]
    rt._dot_action(LOUNGE, "dropin_dial", {"session": "di-rr", "caller": KIT},
                   session_id="di-rr")
    loop = _asyncio.new_event_loop()
    try:
        while hass.jobs:
            jobs, hass.jobs = hass.jobs, []
            for fn, *argv in jobs:
                coro = fn(*argv)
                if _asyncio.iscoroutine(coro):
                    loop.run_until_complete(coro)
    finally:
        loop.close()
    assert seen["return_response"] is True, "answer must be requested"
    assert seen["blocking"] is True


def test_empty_dial_response_is_undelivered_and_tears_down():
    """A dial that answers NOTHING (stale link after a dot reboot) must be
    treated like a failed dial: reap the ringing session now."""
    rt, hass = make_runtime_with_states(lambda service, args: {}, {})

    rt.machine.call("di-empty", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    drain(hass)

    assert "di-empty" not in rt.machine.sessions, \
        "empty dial answer = undelivered ring = torn down"
    assert rt.machine.endpoint_state(LOUNGE) == "idle"
    assert rt.machine.endpoint_state(KIT) == "idle"


def test_state_answer_counts_as_delivered():
    """The dot's {"state": ...} answer (Answers: true on all four call-lane
    actions) is a delivery confirmation — session must stay ringing."""
    rt, hass = make_runtime(lambda service, args: None)  # fake -> {"state": "ringing"}

    rt.machine.call("di-ans", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    drain(hass)
    assert "di-ans" in rt.machine.sessions
    assert rt.machine.endpoint_state(KIT) == "ringing"


def test_undelivered_end_retried_while_sensor_still_in_call(caplog, monkeypatch):
    """An undelivered dropin_end gets a bounded retry (2 tries, 2s apart)
    ONLY while the dot's session sensor still paints an in-call state."""
    import custom_components.echolocal_dropin.runtime as rt_mod
    monkeypatch.setattr(rt_mod.asyncio, "sleep", _async_noop)
    calls: list[str] = []
    rt, hass = make_runtime_with_states(
        lambda service, args: calls.append(service) or {},
        {session_sensor_for(LOUNGE): "in-call"},
    )

    loop = asyncio.new_event_loop()
    try:
        loop.run_until_complete(rt._dot_action_async(
            f"{dot_for(LOUNGE)['node']}_dropin_end", {"reason": "hangup"},
            "di-end", LOUNGE,
        ))
    finally:
        loop.close()

    assert len(calls) == 1 + rt.DOT_END_RETRIES, \
        f"initial + {rt.DOT_END_RETRIES} retries expected, got {len(calls)}"
    assert "UNDELIVERED after 2 retries" in caplog.text


async def _async_noop(_delay):
    return None


def test_undelivered_end_not_retried_once_sensor_idle(monkeypatch, caplog):
    """If the dot's sensor already paints idle/unknown, the end is moot —
    no retry (the retry loop exits before firing again)."""
    import logging
    caplog.set_level(logging.INFO)
    import custom_components.echolocal_dropin.runtime as rt_mod
    monkeypatch.setattr(rt_mod.asyncio, "sleep", _async_noop)
    calls: list[str] = []
    rt, hass = make_runtime_with_states(
        lambda service, args: calls.append(service) or {},
        {session_sensor_for(LOUNGE): "idle"},
    )

    loop = asyncio.new_event_loop()
    try:
        loop.run_until_complete(rt._dot_action_async(
            f"{dot_for(LOUNGE)['node']}_dropin_end", {"reason": "hangup"},
            "di-idle", LOUNGE,
        ))
    finally:
        loop.close()

    assert len(calls) == 1, "sensor idle -> no retry"
    assert "no retry needed" in caplog.text


def test_undelivered_end_retry_delivers_and_stops(monkeypatch, caplog):
    """Retry 1 delivers -> the second retry never fires."""
    import logging
    caplog.set_level(logging.INFO)
    import custom_components.echolocal_dropin.runtime as rt_mod
    monkeypatch.setattr(rt_mod.asyncio, "sleep", _async_noop)
    state = {"n": 0}

    def behavior(service, args):
        state["n"] += 1
        return {} if state["n"] == 1 else {"state": "idle"}

    rt, hass = make_runtime_with_states(
        behavior, {session_sensor_for(LOUNGE): "in-call"},
    )

    loop = asyncio.new_event_loop()
    try:
        loop.run_until_complete(rt._dot_action_async(
            f"{dot_for(LOUNGE)['node']}_dropin_end", {"reason": "hangup"},
            "di-re", LOUNGE,
        ))
    finally:
        loop.close()

    assert state["n"] == 2, "stop after the retry that delivers"
    assert "delivered on retry 1" in caplog.text