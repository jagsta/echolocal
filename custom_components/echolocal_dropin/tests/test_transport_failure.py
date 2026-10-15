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
                         blocking: bool = True) -> None:
        raise NotImplementedError("override self.dial Behavior per test")

    async def async_add_executor_job(self, fn, *args):
        return fn(*args)


def make_runtime(dial_behavior) -> tuple[DropInRuntime, FakeHass]:
    """Build a runtime over a fake hass; dial_behavior(service) -> None|raise."""
    hass = FakeHass()

    async def fake_call(domain, service, args, blocking=True):
        hass.service_calls.append((domain, service, dict(args), blocking))
        return dial_behavior(service, dict(args))

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
    """Execute pending add_job callbacks (coroutine fns) to completion."""
    jobs, hass.jobs = hass.jobs, []
    loop = asyncio.new_event_loop()
    try:
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
    assert any(s.endswith("_end") for _, s, _, _ in hass.service_calls)


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