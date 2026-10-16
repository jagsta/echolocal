"""t_61af01ce: ring-announce cue — "<caller> is dropping in" on the callee.

The HA component speaks the cue at ring via the callee's assist_satellite
announce path (tts.speak, the same route the offline-at-dial notice uses): no
voice turn is opened (no mic consumed — the D-P2P-8 bug class), nothing
touches the P2P offer (only constructed at connect), and it is gated by the
one-way entry option announce_on_ring (default ON).

Run from the REPO ROOT: /usr/bin/python3 -m pytest
custom_components/echolocal_dropin/tests/test_ring_announce.py -q
"""
from __future__ import annotations

import asyncio  # noqa: F401 — drain() comes from test_offline_dial, kept for parity

from custom_components.echolocal_dropin.runtime import DropInRuntime

from .test_offline_dial import KIT, LOUNGE, FakeHass, drain, make_runtime


def make_runtime_with_tts() -> tuple:
    """Runtime whose sync tts service calls are recorded."""
    rt, hass, events = make_runtime()
    rt._media_targets = {
        LOUNGE: "assist_satellite.lounge_assist_satellite",
        KIT: "assist_satellite.kitchen_assist_satellite",
    }
    tts_calls: list[dict] = []

    def fake_tts(domain, service, args, blocking=False):
        tts_calls.append(dict(args))
        hass.service_calls.append((domain, service, dict(args), blocking))

    hass.call = fake_tts  # type: ignore[method-assign]  # tts.speak is a SYNC service call
    hass.async_call = fake_tts  # type: ignore[method-assign]
    return rt, hass, events, tts_calls


def dial(rt: DropInRuntime, caller: str = LOUNGE, caller_room: str = "little_lounge",
         callee: str = KIT, callee_room: str = "kitchen") -> None:
    t = [1000.0]
    rt.machine.now = lambda: t[0]
    rt._call_service({
        "caller_device": caller,
        "caller_room": caller_room,
        "callee_device": callee,
        "callee_room": callee_room,
    })


def test_ring_speaks_room_is_dropping_in_on_callee():
    """Default ON: the callee's satellite gets '<caller room> is dropping in'."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    dial(rt)
    assert len(tts_calls) == 1
    call = tts_calls[0]
    assert call["entity_id"] == "assist_satellite.kitchen_assist_satellite"
    assert call["media_player_entity_id"] == call["entity_id"]
    assert call["message"] == "little lounge is dropping in"
    assert rt.machine.endpoint_state(KIT) == "ringing", "announce does not disturb the machine"


def test_gate_off_no_announce():
    """entry.options announce_on_ring=False: no tts.speak at ring, ring still
    transported (dropin_dial to the callee still fires)."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    rt.announce_on_ring = False
    dial(rt)
    assert tts_calls == []
    assert rt.machine.endpoint_state(KIT) == "ringing"
    # dropin_dial was queued on hass.jobs (hass.add_job is the recording
    # lambda, so the async transport never actually runs and the ring
    # stands); drain() would execute _dot_action_async against a recorder
    # and every dial would fail -> tear down. Assert on the queue.
    assert any(
        "dot_action_async" in str(fn) and "dial" in str(args[0])
        for fn, *args in hass.jobs
    ), f"callee dropin_dial queued, got jobs={hass.jobs}"


def test_entry_option_false_disables():
    """The real __init__ path reads entry.options[announce_on_ring]=False."""
    import types
    rt = DropInRuntime.__new__(DropInRuntime)
    DropInRuntime.__init__(rt, None, types.SimpleNamespace(options={"announce_on_ring": False}))
    assert rt.announce_on_ring is False


def test_entry_option_default_on():
    import types
    rt = DropInRuntime.__new__(DropInRuntime)
    DropInRuntime.__init__(rt, None, types.SimpleNamespace(options={}))
    assert rt.announce_on_ring is True


def test_announce_failure_does_not_tear_down_ring():
    """A raising tts service call must NOT end the ringing session (announce
    is fire-and-forget; only dropin_dial failures tear down)."""
    rt, hass, _events, _tts = make_runtime_with_tts()

    def exploding_tts(*args, **kwargs):
        raise RuntimeError("tts backend down")

    hass.call = exploding_tts  # type: ignore[method-assign]
    hass.async_call = exploding_tts  # type: ignore[method-assign]
    dial(rt)
    assert rt.machine.endpoint_state(KIT) == "ringing", "ring survives an announce failure"
    assert rt.machine.endpoint_state(LOUNGE) == "dialing"


def test_phone_caller_announces_display_name():
    """A phone peer's ring announces its display name, not a room."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    t = [1000.0]
    rt.machine.now = lambda: t[0]
    rt._call_service({
        "caller_device": "phone:jag",
        "caller_room": "phone",
        "callee_device": KIT,
        "callee_room": "kitchen",
    })
    assert len(tts_calls) == 1
    assert tts_calls[0]["message"] == "jag is dropping in"


def test_no_tts_target_announce_skipped():
    """Callee with no registered media target: announce degrades to a log
    line, ring still transported."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    rt._media_targets = {}
    dial(rt, caller=KIT, caller_room="kitchen", callee=LOUNGE,
         callee_room="little_lounge")
    assert tts_calls == []
    assert rt.machine.endpoint_state(LOUNGE) == "ringing"