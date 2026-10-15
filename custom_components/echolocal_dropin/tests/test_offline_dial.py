"""t_3c196f3e: dialing a room with NO dot must not wedge the caller.

Live 15 Oct 2026: the dial automation fed the aspirational slug
`echod-<room>` into echolocal_dropin.call, so a dotless room (spoken
"lounge" -> area big_lounge) registered a RINGING session against a PHANTOM
callee endpoint; the caller sat in DIALING (mid_call -> busy) until the 30s
gc ring timeout, and every retry in the window was busy-rejected.

The contract (recorded in specs/dropin-decisions.md D-DI-6): the
offline-at-dial path IS the dial contract — registered as
`echolocal_dropin.dial` (= runtime.call_or_report_offline) with the callee
resolved up front via area_aliases.json; a dotless/unknown room is a clean
CallError BEFORE any session exists, an OFFLINE dot is TTS at the caller with
no session, and a known dot dials normally.

Run from the REPO ROOT: /usr/bin/python3 -m pytest
custom_components/echolocal_dropin/tests -q
"""
from __future__ import annotations

import asyncio
from typing import Any

from custom_components.echolocal_dropin.device_map import dot_for
from custom_components.echolocal_dropin.runtime import DropInRuntime
from custom_components.echolocal_dropin.state_machine import (
    CallError,
    Endpoint,
    PermissionMap,
    CallStateMachine,
)
from custom_components.echolocal_dropin import const

LOUNGE = "17b8d542d93b7f00a01a79c7e9edcf60"
KIT = "acf8f22077b452856f9ef46024f95d57"


class FakeHass:
    def __init__(self) -> None:
        self.states = type("S", (), {"get": staticmethod(lambda eid: None)})()
        self.jobs: list[tuple] = []
        self.service_calls: list[tuple] = []
        self.services = self

    def add_job(self, *args):
        self.jobs.append(args)

    async def async_call(self, domain: str, service: str, args: dict,
                         blocking: bool = True) -> None:
        self.service_calls.append((domain, service, dict(args), blocking))

    async def async_add_executor_job(self, fn, *args):
        return fn(*args)

    def call(self, domain: str, service: str, args: dict, blocking: bool = False):
        self.service_calls.append((domain, service, dict(args), blocking))


def make_runtime() -> tuple[DropInRuntime, FakeHass, list]:
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


def call_dial(rt: DropInRuntime, callee_room: str,
              caller: str = LOUNGE, caller_room: str = "little_lounge") -> None:
    """Invoke the dial service impl exactly as the HA automation does."""
    rt._dial_service({
        "caller_device": caller,
        "caller_room": caller_room,
        "callee_room": callee_room,
    })


# ---------------------------------------------------------------------------
# The wedge: a dotless room must NEVER register a phantom session
# ---------------------------------------------------------------------------


def test_dial_dotless_room_refused_no_session():
    """'lounge' -> area big_lounge has NO dot: clean CallError, no session,
    caller stays idle (never DIALING), so a retry is not busy-rejected."""
    rt, _hass, events = make_runtime()
    try:
        call_dial(rt, "big_lounge")
        raise AssertionError("dial to a dotless room must raise")
    except CallError:
        pass
    assert rt.machine.sessions == {}, "no phantom session may exist"
    assert rt.machine.endpoint_state(LOUNGE) == "idle"
    assert not any(kind in ("ring", "busy_chime") for kind, _ in events)


def test_dial_dotless_room_second_attempt_not_busy():
    """The acceptance CHECK: dial dotless room, then AGAIN 2s later — the
    second dial must not be busy-rejected (caller never wedged)."""
    rt, _hass, _events = make_runtime()
    for _ in range(2):
        try:
            call_dial(rt, "big_lounge")
        except CallError:
            pass
    assert rt.machine.endpoint_state(LOUNGE) == "idle"


def test_dial_unknown_room_refused():
    """A room that is neither an alias nor an area is refused outright."""
    rt, _hass, _events = make_runtime()
    try:
        call_dial(rt, "narnia")
        raise AssertionError("unknown room must raise")
    except CallError:
        pass
    assert rt.machine.sessions == {}


def test_dial_resolves_spoken_alias_lounge_to_lounge_dot():
    """The alias table maps spoken 'lounge' -> big_lounge (dotless) BUT
    'little lounge'/'small lounge' -> little_lounge (THE lounge dot)."""
    rt, _hass, _events = make_runtime()
    t = [1000.0]
    rt.machine.now = lambda: t[0]
    call_dial(rt, "little lounge", caller=KIT, caller_room="kitchen")
    assert rt.machine.sessions, "session exists"
    session = next(iter(rt.machine.sessions.values()))
    callee_ids = {ep.device_id for ep in session.endpoints} - {LOUNGE}
    assert callee_ids == {KIT}, f"lounge dot dialed, got {callee_ids}"
    # device resolution used the alias, and dot_for() proves it is a real dot
    assert dot_for(KIT) is not None


def test_dial_with_explicit_dot_device_dials_normally():
    """Regression: the working path (t_23673b6d) still rings."""
    rt, hass, events = make_runtime()
    t = [1000.0]
    rt.machine.now = lambda: t[0]
    call_dial(rt, "kitchen")
    assert next(iter(rt.machine.sessions.values())).state == "ringing"
    assert rt.machine.endpoint_state(KIT) == "ringing"
    assert rt.machine.endpoint_state(LOUNGE) == "dialing"
    assert any(kind == "ring" for kind, _ in events)
    drain(hass)
    assert any(s.endswith("_dial") for _, s, _, _ in hass.service_calls), \
        "the callee dot received its dropin_dial"


def test_dial_offline_dot_tts_not_session():
    """device_available = False -> TTS notice at the caller, NO session."""
    rt, hass, events = make_runtime()
    tts_calls: list[dict] = []

    def fake_tts(domain, service, args, blocking=False):
        tts_calls.append(dict(args))
        hass.service_calls.append((domain, service, dict(args), blocking))

    hass.call = fake_tts  # type: ignore[method-assign]  # _tts_on_caller uses the SYNC service API
    hass.async_call = fake_tts  # type: ignore[method-assign]
    rt.device_available = lambda device_id: False
    rt._media_targets = {LOUNGE: "assist_satellite.lounge_assist_satellite"}
    offline_events: list[dict] = []
    real_runtime_emit = rt.emit

    def recording_emit(kind, data):
        if kind == "offline_at_dial":
            offline_events.append(dict(data))
        return real_runtime_emit(kind, data)

    rt.emit = recording_emit  # type: ignore[method-assign]
    call_dial(rt, "kitchen", caller=LOUNGE)
    assert rt.machine.sessions == {}, "offline callee: no session"
    assert len(tts_calls) == 1
    assert "kitchen isn't available" in tts_calls[0]["message"]
    assert offline_events, "offline_at_dial recorded"


def test_dial_slash_alias_lowercase_normalized():
    """Alias resolution is whitespace/quote-normalized (intent_router rules)."""
    rt, _hass, _events = make_runtime()
    try:
        call_dial(rt, "Living Room")  # alias for big_lounge (dotless)
        raise AssertionError("must raise (dotless target)")
    except CallError:
        pass
    assert rt.machine.sessions == {}


def test_call_service_still_accepts_aspirational_slug_for_dot_areas():
    """_call_service keeps its Card-A back-compat: echod-<area> slugs for
    areas WITH dots still resolve (existing behaviour unchanged)."""
    rt, _hass, _events = make_runtime()
    rt._call_service({
        "caller_device": LOUNGE,
        "caller_room": "little_lounge",
        "callee_device": "echod-kitchen",
        "callee_room": "kitchen",
    })
    assert rt.machine.endpoint_state(KIT) == "ringing"