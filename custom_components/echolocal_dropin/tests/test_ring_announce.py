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

import asyncio
import time
from types import SimpleNamespace  # noqa: F401

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
    """Default ON: the callee's SPEAKER media_player gets 'lounge is dropping
    in' (t_79ebd16b: the dot's media_player announce claim coexists with the
    ring; the assist_satellite is transiently unavailable during ring — live
    16 Oct 13:34). Message uses the caller dot's announce_name. The cue is
    now spoken from the announce-then-dial job (speak-before-ring): run the
    announce job only (the dial recorder would fail its transport and tear
    the session down — not this test's subject)."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    dial(rt)
    fn, ring_data = announce_jobs(hass)[0]
    _run_async(fn(ring_data))
    assert len(tts_calls) == 1
    call = tts_calls[0]
    assert call["entity_id"] == "media_player.kitchen_kitchen_speaker"
    # t_79ebd16b: 'lounge' (announce_name), not the raw 'little lounge'.
    assert "lounge is dropping in" in call["media_content_id"]
    assert rt.machine.endpoint_state(KIT) == "ringing", "announce does not disturb the machine"


def test_ring_announce_uses_play_media_on_speaker():
    """t_79ebd16b: the mapped speaker route is media_player.play_media with
    media_content_type=announce (tts.speak rejects this player — the ESPHome
    integration maps its feature flags without MediaPlayerEntityFeature.ANNOUNCE,
    'missing or not currently available' every call, live 16 Oct 14:24-14:25)."""
    rt, hass, _events, _tts = make_runtime_with_tts()
    dial(rt)
    drain(hass)
    plays = [c for c in hass.service_calls if c[0] == "media_player" and c[1] == "play_media"]
    assert len(plays) == 1
    _domain, _service, args, _blocking = plays[0]
    assert args["entity_id"] == "media_player.kitchen_kitchen_speaker"
    assert args["media_content_type"] == "announce"
    assert "lounge is dropping in" in args["media_content_id"]
    assert "media-source://tts/" in args["media_content_id"]


def test_announce_falls_back_to_satellite_without_speaker_entity():
    """A dot without a speaker_entity in the map keeps the (old) satellite
    route and the unartiled message, so the cue degrades rather than dies."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    # Simulate an unmapped-dot speaker entity: strip the map entry by using a
    # callee whose facts have no speaker_entity yet (monkeypatch a copy).
    from custom_components.echolocal_dropin import device_map as dm
    saved = dict(dm.DROPIN_DEVICE_MAP[KIT])
    dm.DROPIN_DEVICE_MAP[KIT] = {k: v for k, v in saved.items() if k != "speaker_entity"}
    try:
        dial(rt)
        fn, ring_data = announce_jobs(hass)[0]
        _run_async(fn(ring_data))
        assert len(tts_calls) == 1
        call = tts_calls[0]
        assert call["entity_id"] == "assist_satellite.kitchen_assist_satellite"
        # name normalization (announce_name) applies on BOTH paths
        assert call["message"] == "lounge is dropping in"
    finally:
        dm.DROPIN_DEVICE_MAP[KIT] = saved


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
    fn, ring_data = announce_jobs(hass)[0]
    _run_async(fn(ring_data))
    assert len(tts_calls) == 1
    assert "jag is dropping in" in tts_calls[0]["media_content_id"]


def test_no_tts_target_announce_skipped():
    """Callee with no registered media target: announce degrades to a log
    line, ring still transported."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    rt._media_targets = {}
    dial(rt, caller=KIT, caller_room="kitchen", callee=LOUNGE,
         callee_room="little_lounge")
    assert tts_calls == []
    assert rt.machine.endpoint_state(LOUNGE) == "ringing"


# ---------------------------------------------------------------------------
# t_903017d5: speak-BEFORE-ring + availability gate (R1a/R2)
# ---------------------------------------------------------------------------


def _run_async(coro) -> None:
    loop = asyncio.new_event_loop()
    try:
        loop.run_until_complete(coro)
    finally:
        loop.close()


def announce_jobs(hass: FakeHass) -> list:
    """The (fn, ring_data) pairs queued for the announce-then-dial handoff."""
    return [(fn, rest[0] if rest else {}) for fn, *rest in hass.jobs
            if "announce_then_dial" in str(fn) and rest]


def dial_jobs(hass: FakeHass) -> list:
    """dropin_dial dispatches recorded via hass.jobs (the sync add_job of
    the async_dot_action call in _dispatch_ring)."""
    return [(fn, *rest) for fn, *rest in hass.jobs
            if "dot_action" in str(fn) or ("dot_action" in str(rest))]


def test_speak_before_ring_dial_dispatched_after_announce():
    """R1a: the ring event no longer dispatches dropin_dial inline — only an
    announce-then-dial job is queued; running that job speaks the cue BEFORE
    the dial is queued."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    dial(rt)
    # The dial has NOT been queued yet (speak-before-ring), announce is queued.
    assert len(announce_jobs(hass)) == 1, "announce-then-dial job queued"
    jobs_before = len(hass.jobs)
    assert jobs_before == 1, f"only the announce job; jobs={hass.jobs}"

    # Run the announce job: it speaks, THEN queues the dial.
    fn, ring_data = announce_jobs(hass)[0]
    _run_async(fn(ring_data))
    assert len(tts_calls) == 1, "cue spoken"
    assert any(
        "dot_action_async" in str(fn2) and "dial" in str(args[0])
        for fn2, *args in hass.jobs
    ), f"dropin_dial dispatched after announce; jobs={hass.jobs}"
    assert rt.machine.endpoint_state(KIT) == "ringing"


def test_announce_gate_waits_for_unavailable_satellite_then_dials():
    """R2: an unavailable satellite blocks the speak only BOUNDED (<=10s);
    on expiry a WARN is logged and the dial STILL fires — announce must
    never block or break dialing."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    rt.ANNOUNCE_AVAILABILITY_WAIT_SECONDS = 0.3  # test-scale bound

    # Simulate a satellite that never becomes available.
    class _DeadState:
        state = "unavailable"

    hass.states = SimpleNamespace(get=lambda eid: _DeadState())
    dial(rt)
    fn, ring_data = announce_jobs(hass)[0]
    t0 = time.monotonic()
    _run_async(fn(ring_data))
    waited = time.monotonic() - t0
    assert tts_calls == [], "no cue to an unavailable satellite"
    assert waited < 3.0, f"bounded wait, not a block (waited {waited:.1f}s)"
    assert any(
        "dot_action_async" in str(fn2) and "dial" in str(args[0])
        for fn2, *args in hass.jobs
    ), "ring still fires after the gate expires"
    assert rt.machine.endpoint_state(KIT) == "ringing"


def test_announce_gate_waits_while_satellite_transitions_to_available():
    """R2: a satellite that becomes available DURING the wait gets the cue —
    the wait polls, it does not bail on the first unavailable sight."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    rt.ANNOUNCE_AVAILABILITY_POLL_SECONDS = 0.05

    flips = {"n": 0}

    class _State:
        state = "unavailable"

    def flippy(eid):
        flips["n"] += 1
        if flips["n"] >= 3:  # available on the 3rd poll
            _State.state = "idle"
        return _State

    hass.states = SimpleNamespace(get=flippy)
    dial(rt)
    fn, ring_data = announce_jobs(hass)[0]
    _run_async(fn(ring_data))
    assert len(tts_calls) == 1, "cue spoken once the satellite came back"
    assert any(
        "dot_action_async" in str(fn2) and "dial" in str(args[0])
        for fn2, *args in hass.jobs
    )


def test_announce_gate_proceeds_when_satellite_available():
    """R2 happy path: satellite available -> the cue is spoken, then dial."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()

    class _State:
        state = "idle"

    hass.states = SimpleNamespace(get=lambda eid: _State())
    dial(rt)
    fn, ring_data = announce_jobs(hass)[0]
    _run_async(fn(ring_data))
    assert len(tts_calls) == 1
    assert any(
        "dot_action_async" in str(fn2) and "dial" in str(args[0])
        for fn2, *args in hass.jobs
    )


def test_gate_off_dials_immediately_no_announce():
    """announce_on_ring=False keeps the OLD inline path: dial queued on the
    ring event itself, no announce job at all."""
    rt, hass, _events, tts_calls = make_runtime_with_tts()
    rt.announce_on_ring = False
    dial(rt)
    assert tts_calls == []
    assert announce_jobs(hass) == []
    assert any(
        "dot_action_async" in str(fn) and "dial" in str(args[0])
        for fn, *args in hass.jobs
    ), f"inline dropin_dial queued; jobs={hass.jobs}"


def test_hanging_tts_cannot_stall_dial_within_cap():
    """t_650b2f99 (the 16 Oct 16:56 freeze): a TTS service call that NEVER
    returns (blocked backend / wedged media session) must not wedge the
    event loop — the announce is capped at ANNOUNCE_TTS_CAP_SECONDS and the
    dial dispatches within ~the cap. The sync hass.services.call() could
    not be interrupted at all; the async API under asyncio.wait_for can."""
    rt, hass, _events, _tts = make_runtime_with_tts()
    rt.ANNOUNCE_TTS_CAP_SECONDS = 0.2  # test-scale cap
    started = {"n": 0}

    async def hanging_async_call(*args, **kwargs):
        started["n"] += 1
        await asyncio.sleep(60)  # simulates the wedged play_media

    hass.async_call = hanging_async_call  # type: ignore[method-assign]
    dial(rt)
    fn, ring_data = announce_jobs(hass)[0]
    t0 = time.monotonic()
    _run_async(fn(ring_data))
    elapsed = time.monotonic() - t0
    assert started["n"] == 1, "the (async) speak was attempted"
    assert elapsed < 3.0, f"dial dispatched within ~cap, not wedged (took {elapsed:.2f}s)"
    assert any(
        "dot_action_async" in str(fn2) and "dial" in str(args[0])
        for fn2, *args in hass.jobs
    ), "dropin_dial dispatched despite the hung announce"
    assert rt.machine.endpoint_state(KIT) == "ringing"


def test_no_sync_service_call_api_in_announce_paths():
    """t_650b2f99 acceptance: the announce/tts paths must use the async
    service API only — grep the runtime source for the SYNC
    hass.services.call( outside comments/docstrings (it cannot be
    interrupted once wedged, which froze the loop 16 Oct 16:56)."""
    import inspect
    src = inspect.getsource(DropInRuntime)
    for lineno, line in enumerate(src.splitlines(), 1):
        code = line.split("#", 1)[0]  # drop trailing comments
        if not code.strip() or not code.strip().rstrip().endswith(")"):
            continue  # docstring/comment lines and multi-line calls read at the closing paren
        assert ".services.call(" not in code or ".services.async_call(" in code, (
            f"runtime line {lineno} uses the SYNC service API: {line.strip()}"
        )