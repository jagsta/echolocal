"""Dot-native accept must notify HA (t_108fc818).

Live 15 Oct 11:47: Jag pressed the kitchen action button at ~T+17s; the dot
accepted DOT-SIDE (kitchen session sensor InCall, turn open) but HA's machine
stayed RINGING — the ring event at 12:47:02 was followed only by the 12:47:36
ring_timeout teardown: no connect event, StarRelay never opened, neither dot
was dropin_connect'ed, no audio anywhere. The dots publish their session state
ONLY as a TextSensor, and runtime.py read it on demand (utterance resolution)
but never LISTENED. These tests pin the subscription behaviour:

  - dot sensor -> in_call while the HA session is RINGING  => machine.accept
    for the CALLEE, connect event with BOTH endpoint ids, dropin_connect to
    both dots (the machine's connect path is the only relay opener);
  - the "ringing" paint is our own dial echo and must NOT accept;
  - a device already ACTIVE / not the ringing callee must NOT produce a second
    connect (idempotence), incl. the ring -> in-call -> muted sequence.

Run from the REPO ROOT only (python3 -m pytest
custom_components/echolocal_dropin/tests -q); running inside the component dir
fails with ModuleNotFoundError.
"""
from __future__ import annotations

import asyncio
from types import SimpleNamespace
from typing import Any

from custom_components.echolocal_dropin.const import (
    RING_FLOOR_SECONDS,
    SESSION_ACTIVE,
    SESSION_RINGING,
)
from custom_components.echolocal_dropin.device_map import session_sensor_for
from custom_components.echolocal_dropin.runtime import DropInRuntime
from custom_components.echolocal_dropin.state_machine import (
    CallStateMachine,
    Endpoint,
    PermissionMap,
)

LOUNGE = "17b8d542d93b7f00a01a79c7e9edcf60"
KIT = "acf8f22077b452856f9ef46024f95d57"
LOUNGE_SENSOR = session_sensor_for(LOUNGE)
KIT_SENSOR = session_sensor_for(KIT)


def _st(value: str) -> Any:
    """Minimal HA State stand-in (only .state is read)."""
    return SimpleNamespace(state=value)


class FakeHass:
    """Minimal hass stand-in: states.get + add_job + async service calls."""

    def __init__(self) -> None:
        self.states = SimpleNamespace(get=lambda entity_id: None)
        self.jobs: list[tuple] = []
        self.service_calls: list[tuple] = []
        self.services = self

    def add_job(self, *args) -> None:
        self.jobs.append(args)

    async def async_call(self, domain: str, service: str, args: dict,
                         blocking: bool = True) -> None:
        self.service_calls.append((domain, service, dict(args), blocking))

    async def async_add_executor_job(self, fn, *args):
        return fn(*args)


def make_runtime() -> tuple[DropInRuntime, FakeHass, list]:
    """A runtime over a fake hass that records every machine event."""
    hass = FakeHass()
    rt = DropInRuntime.__new__(DropInRuntime)
    rt.hass = hass  # type: ignore[assignment]
    rt.entry = None
    rt.permissions = PermissionMap()
    events: list[tuple[str, dict]] = []

    def emit(kind: str, data: dict) -> None:
        events.append((kind, dict(data)))
        rt._on_event(kind, dict(data))

    rt.machine = CallStateMachine(permissions=rt.permissions, emit=emit)
    rt.relay = None
    rt._relays = {}
    rt._media_targets = {}
    rt._paused_for_call = set()
    rt.device_available = None
    return rt, hass, events


def drain(hass: FakeHass) -> None:
    """Run pending add_job callbacks (the fire-and-forget dot transport)."""
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


def ringing_pair(rt: DropInRuntime, session_id: str = "di-btn") -> list:
    """A lounge -> kitchen session, past the ring floor, still RINGING."""
    clock = [1000.0]
    rt.machine.now = lambda: clock[0]
    rt.machine.call(
        session_id, Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"),
    )
    clock[0] += RING_FLOOR_SECONDS + 1
    assert rt.machine.endpoint_state(KIT) == "ringing"
    assert rt.machine.endpoint_state(LOUNGE) == "dialing"
    return clock


def connect_events(events: list) -> list[dict]:
    return [data for kind, data in events if kind == "connect"]


# ---------------------------------------------------------------------------
# The bug: a dot-side accept must reach the machine and open the relay
# ---------------------------------------------------------------------------


def test_dot_sensor_in_call_accepts_and_emits_connect_for_both_endpoints():
    rt, hass, events = make_runtime()
    clock = ringing_pair(rt)

    # The kitchen action button: dot-side accept paints the sensor in-call.
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("in-call"), at=clock[0],
    )

    assert rt.machine.endpoint_state(KIT) == "in_call", "callee connected"
    assert rt.machine.endpoint_state(LOUNGE) == "in_call", "caller connected"
    assert rt.machine.sessions["di-btn"].state == SESSION_ACTIVE

    connects = connect_events(events)
    assert len(connects) == 1, "exactly one connect"
    assert set(connects[0]["endpoints"]) == {LOUNGE, KIT}, "BOTH endpoint ids"

    # ... and the machine's connect event is what transports the join to both
    # dots: caller gets dropin_connect (with peer), callee dropin_answer
    # (t_01e7db90 — HA's schema requires peer on dropin_connect and the callee
    # must never offer).
    drain(hass)
    services = [service for _, service, _, _ in hass.service_calls]
    assert "lounge_dropin_connect" in services
    assert "kitchen_dropin_answer" in services
    assert "kitchen_dropin_connect" not in services


def test_ringing_sensor_transition_is_the_dial_echo_not_an_accept():
    """The dot painting Ringing is OUR dial echo — accepting it would answer
    our own ring."""
    rt, _hass, events = make_runtime()
    clock = ringing_pair(rt)

    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("idle"), _st("ringing"), at=clock[0],
    )

    assert rt.machine.sessions["di-btn"].state == SESSION_RINGING
    assert rt.machine.endpoint_state(KIT) == "ringing"
    assert connect_events(events) == []


def test_caller_sensor_paint_is_ignored():
    """Only the CALLEE's accepted paint accepts: the caller's endpoint is
    STATE_DIALING, so its sensor can never match a ringing callee."""
    rt, _hass, events = make_runtime()
    clock = ringing_pair(rt)

    rt.handle_session_sensor_change(
        LOUNGE_SENSOR, _st("idle"), _st("in-call"), at=clock[0],
    )

    assert rt.machine.sessions["di-btn"].state == SESSION_RINGING
    assert connect_events(events) == []


# ---------------------------------------------------------------------------
# Idempotence (no second connect for an accepted session)
# ---------------------------------------------------------------------------


def test_accept_on_active_session_emits_no_duplicate_connect():
    rt, _hass, events = make_runtime()
    clock = ringing_pair(rt)

    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("in-call"), at=clock[0],
    )
    assert len(connect_events(events)) == 1

    # A duplicated in-call paint, then the dot's own mute sub-state paint:
    # the session is ACTIVE, so neither may re-accept.
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("in-call"), _st("in-call"), at=clock[0],
    )
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("in-call"), _st("muted"), at=clock[0] + 1,
    )

    assert len(connect_events(events)) == 1, "no duplicate connect"
    assert rt.machine.sessions["di-btn"].state == SESSION_ACTIVE


def test_active_session_gets_no_further_accept_attempts():
    """Repeated accepted-state paints after the connect must not even reach
    machine.accept again (the session is no longer RINGING): the ring floor is
    already enforced dot-side, and nothing here may re-dial or re-accept."""
    rt, _hass, _events = make_runtime()
    clock = ringing_pair(rt)
    attempts: list[tuple] = []
    real_accept = rt.machine.accept

    def recording(session_id, device_id, at=None):
        attempts.append((session_id, device_id))
        return real_accept(session_id, device_id, at=at)

    rt.machine.accept = recording  # type: ignore[method-assign]

    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("in-call"), at=clock[0],
    )
    # same session, dots repaint accepted states (in-call again, then muted)
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("in-call"), _st("in-call"), at=clock[0],
    )
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("in-call"), _st("muted"), at=clock[0] + 1,
    )

    assert attempts == [("di-btn", KIT)], "exactly one accept attempt"


def test_session_sensor_entity_ids_map_back_to_devices():
    """The subscription is only as good as the sensor->device mapping: a
    renamed sensor in device_map must fail here, not silently no-op the
    bridge."""
    rt, _hass, _events = make_runtime()
    assert rt._device_for_session_sensor(KIT_SENSOR) == KIT
    assert rt._device_for_session_sensor(LOUNGE_SENSOR) == LOUNGE
    assert rt._device_for_session_sensor("sensor.nope") is None
    assert rt._device_for_session_sensor(None) is None


def test_unknown_sensor_or_idle_transition_is_ignored():
    rt, _hass, events = make_runtime()
    clock = ringing_pair(rt)

    rt.handle_session_sensor_change(
        "sensor.not_a_dot_session", _st("idle"), _st("in-call"), at=clock[0],
    )
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("idle"), at=clock[0],
    )

    assert rt.machine.sessions["di-btn"].state == SESSION_RINGING
    assert connect_events(events) == []
