"""A dot painting idle mid-call must end the HA session and notify the peer.

Live 15 Oct 15:33/15:34 (t_a57913f9 round 2): the dot-side hangup control is
volume-down → wiring.Session.End ("hangup") (wiring.go:210-222). It ends the
LOCAL dot correctly — end chime, turn released, leg closed, local sensor idle —
but HA was never told, so the peer's leg died on ICE teardown while its session,
sensor, LED and turn stayed in-call with no audio until the 30-min
max_call_seconds reaper (kitchen volume-down stranded the lounge 34.3 s; lounge
volume-down stranded the kitchen 13.2 s). D-DI-5 says a callee ends on
'hang up'/'stop'/button, so the stranding contradicts a decided behaviour.

These tests pin the mirror: an accepted-state → idle transition on the dot's
session sensor, while that endpoint is in-call on an ACTIVE session, drives the
SAME machine.hangup() path the HA service uses (teardown event + dropin_end to
both dots), and stays a no-op in every other shape.

Run from the REPO ROOT only (python3 -m pytest
custom_components/echolocal_dropin/tests -q).
"""
from __future__ import annotations

from custom_components.echolocal_dropin.const import SESSION_RINGING
from custom_components.echolocal_dropin.device_map import session_sensor_for
from custom_components.echolocal_dropin.runtime import DropInRuntime

from .test_dot_accept import (  # noqa: F401 — shared harness
    KIT,
    KIT_SENSOR,
    LOUNGE,
    LOUNGE_SENSOR,
    _st,
    connect_events,
    drain,
    make_runtime,
    ringing_pair,
)


def active_pair(rt: DropInRuntime, session_id: str = "di-btn") -> list:
    """A lounge -> kitchen session accepted dot-side into ACTIVE."""
    clock = ringing_pair(rt, session_id)
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("in-call"), at=clock[0],
    )
    assert rt.machine.sessions[session_id].state == "active"
    return clock


def teardown_events(events: list) -> list[dict]:
    return [data for kind, data in events if kind == "teardown"]


# ---------------------------------------------------------------------------
# The fix: a dot-side end reaches the machine and both dots
# ---------------------------------------------------------------------------


def test_callee_dot_idle_ends_the_session_and_notifies_both_dots():
    rt, hass, events = make_runtime()
    clock = active_pair(rt)

    # Kitchen volume-down: the dot ends local-side and paints idle.
    result = rt.handle_session_sensor_change(
        KIT_SENSOR, _st("in-call"), _st("idle"), at=clock[0] + 5,
    )

    assert result == "di-btn", "the mirror reports the session it ended"
    assert "di-btn" not in rt.machine.sessions, "session torn down, not stranded"
    assert rt.machine.endpoint_state(KIT) == "idle"
    assert rt.machine.endpoint_state(LOUNGE) == "idle", "peer released too"

    tears = teardown_events(events)
    assert len(tears) == 1, "exactly one teardown"
    assert set(tears[0]["endpoints"]) == {LOUNGE, KIT}
    assert tears[0]["reason"] == "hangup"

    # ... and the machine's teardown is what ends the peer physically.
    drain(hass)
    services = [service for _, service, _, _ in hass.service_calls]
    assert services.count("lounge_dropin_end") >= 1, "peer gets dropin_end"
    assert services.count("kitchen_dropin_end") >= 1


def test_caller_dot_idle_ends_the_session_too():
    """No role gating: the caller's volume-down hangs up the same way."""
    rt, _hass, events = make_runtime()
    clock = active_pair(rt)

    result = rt.handle_session_sensor_change(
        LOUNGE_SENSOR, _st("in-call"), _st("idle"), at=clock[0] + 5,
    )

    assert result == "di-btn"
    assert "di-btn" not in rt.machine.sessions
    assert len(teardown_events(events)) == 1


def test_muted_to_idle_is_also_a_dot_side_end():
    """Mute is an in-call sub-state; muting then hanging up still ends it."""
    rt, _hass, _events = make_runtime()
    clock = active_pair(rt)

    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("in-call"), _st("muted"), at=clock[0] + 1,
    )
    result = rt.handle_session_sensor_change(
        KIT_SENSOR, _st("muted"), _st("idle"), at=clock[0] + 2,
    )

    assert result == "di-btn"
    assert rt.machine.sessions == {}


# ---------------------------------------------------------------------------
# Guarding: nothing else may end a session
# ---------------------------------------------------------------------------


def test_ringing_to_idle_is_not_mirrored():
    """The dot declining our dial echo (or its own ring timeout) is not an
    in-call end: the machine's ring_timeout owns that, and it must not be
    short-circuited into a hangup of a session nobody answered."""
    rt, _hass, events = make_runtime()
    clock = ringing_pair(rt)

    result = rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("idle"), at=clock[0] + 5,
    )

    assert result is None
    assert rt.machine.sessions["di-btn"].state == SESSION_RINGING
    assert teardown_events(events) == []


def test_idle_republished_while_idle_ends_nothing():
    """Boot / reconnect / unavailable -> idle paints are not hangsups."""
    rt, _hass, events = make_runtime()
    clock = active_pair(rt)

    for old in ("idle", "unavailable", "unknown"):
        assert rt.handle_session_sensor_change(
            KIT_SENSOR, _st(old), _st("idle"), at=clock[0] + 5,
        ) is None

    assert "di-btn" in rt.machine.sessions, "session untouched"
    assert teardown_events(events) == []


def test_ha_hangup_then_dot_idle_paint_is_a_no_op():
    """Ordering: HA ends the session, its own dropin_end makes both dots
    repaint idle — those late paints must not emit a second teardown."""
    rt, _hass, events = make_runtime()
    clock = active_pair(rt)

    rt.machine.hangup("di-btn", at=clock[0] + 5)
    assert len(teardown_events(events)) == 1

    for sensor, old in ((KIT_SENSOR, "in-call"), (LOUNGE_SENSOR, "in-call")):
        assert rt.handle_session_sensor_change(
            sensor, _st(old), _st("idle"), at=clock[0] + 6,
        ) is None

    assert len(teardown_events(events)) == 1, "no second teardown"


def test_end_is_scoped_to_the_ending_dot():
    """A dot with no in-call endpoint in any ACTIVE session ends nothing —
    the sensor of an unrelated device cannot hang up someone else's call."""
    rt, _hass, events = make_runtime()
    clock = active_pair(rt)

    # Unknown sensor: ignored outright.
    assert rt.handle_session_sensor_change(
        "sensor.not_a_dot_session", _st("in-call"), _st("idle"), at=clock[0] + 5,
    ) is None
    assert "di-btn" in rt.machine.sessions
    assert teardown_events(events) == []
