"""t_47f64032 (D-ANN 2/3): the announce orchestration service.

Card 2 acceptance pinned in tests: target resolution per D-ANN-2 (invoking
dot excluded; bare = all other dots; "announce to <room>" = that dot; bad
shapes refused loudly), services.yaml declares the service, and the
orchestration flow kicks dropin_announce on the origin, polls capture
completion, then nudges each target's dropin_play — async_call EVERYWHERE
(t_650b2f99).

Run from the REPO ROOT: python3 -m pytest
custom_components/echolocal_dropin/tests -q (inside the dir fails
ModuleNotFoundError).
"""
from __future__ import annotations

import asyncio
from typing import Any

import yaml

from custom_components.echolocal_dropin.const import (
    DOT_ANNOUNCE_PATH,
    DOT_ANNOUNCE_PORT,
)
from custom_components.echolocal_dropin.runtime import DropInRuntime
from custom_components.echolocal_dropin.state_machine import CallError

from .test_device_map import JASMINE as JAS
from .test_offline_dial import KIT, LOUNGE, FakeHass, make_runtime


def make_announce_runtime(services: dict[tuple, Any] | None = None):
    """Runtime + FakeHass whose async_call records esphome calls; a handler
    dict maps (domain, service) -> return value so tests can pin the dot's
    action answers (the dropin_announce/dropin_play lane-state replies)."""
    rt, hass, events = make_runtime()
    calls: list[tuple[str, str, dict]] = []

    async def fake_async_call(domain: str, service: str, data: dict,
                              blocking: bool = True, **kw):
        calls.append((domain, service, dict(data)))
        return (services or {}).get((domain, service), {"state": "idle"})

    hass.async_call = fake_async_call  # type: ignore[method-assign]
    hass.created_tasks: list = []  # type: ignore[attr-defined]
    rt._aliases = lambda: {}  # no alias table: exact-area ids resolve, spoken aliases don't
    rt._media_targets = {}
    return rt, hass, calls


# ---------------------------------------------------------------------------
# D-ANN-2: target resolution
# ---------------------------------------------------------------------------


def test_bare_announce_targets_all_others():
    rt, _hass, _calls = make_announce_runtime()
    three = {LOUNGE, KIT, JAS}
    assert set(rt._resolve_announce_targets({"caller_device": LOUNGE})) == three - {LOUNGE}
    assert set(rt._resolve_announce_targets({"caller_device": KIT})) == three - {KIT}
    assert set(rt._resolve_announce_targets({"caller_device": JAS})) == three - {JAS}


def test_targeted_announce_resolves_room():
    rt, _hass, _calls = make_announce_runtime()
    assert rt._resolve_announce_targets(
        {"caller_device": LOUNGE, "target_room": "kitchen"}) == [KIT]
    # the invoking dot IS the target: refused, not a silent no-op
    try:
        rt._resolve_announce_targets(
            {"caller_device": LOUNGE, "target_room": "little_lounge"})
    except CallError:
        pass
    else:
        raise AssertionError("announcing to the invoking dot must be refused")


def test_bad_shapes_refused_loudly():
    """D-ANN-2: wrong shapes are CallErrors (loud), never a silent 200 []."""
    rt, _hass, _calls = make_announce_runtime()
    for bad in (
        {},  # no caller at all
        {"caller_device": "not-a-dot"},
        {"caller_device": LOUNGE, "target_room": "big_lounge"},  # dotless room
        {"caller_device": LOUNGE, "target_room": "atlantis"},
    ):
        try:
            rt._resolve_announce_targets(bad)
        except CallError:
            continue
        raise AssertionError(f"bad shape not refused: {bad}")


def test_announce_service_declared():
    """services.yaml declares the announce service with the caller field, and
    async_setup registers SERVICE_ANNOUNCE on the integration."""
    import inspect
    from pathlib import Path

    import custom_components.echolocal_dropin.runtime as rmod

    src = inspect.getsource(rmod.DropInRuntime.async_setup)
    assert "SERVICE_ANNOUNCE" in src
    comp_dir = Path(rmod.__file__).parent
    doc = yaml.safe_load((comp_dir / "services.yaml").read_text())
    assert "announce" in doc, "services.yaml must declare the announce service"
    assert "caller_device" in doc["announce"]["fields"]


# ---------------------------------------------------------------------------
# The orchestration flow: kick -> capture wait -> nudge each target
# ---------------------------------------------------------------------------


def test_announce_flow_kicks_origin_then_nudges_targets():
    rt, _hass, calls = make_announce_runtime()

    async def instant_capture(origin_host):
        return "ann-test"

    rt._await_capture = instant_capture  # type: ignore[method-assign]
    loop = asyncio.new_event_loop()
    try:
        loop.run_until_complete(rt._run_announce(
            {"caller_device": LOUNGE, "target_room": ""}, [KIT]))
    finally:
        loop.close()
    esphome_calls = [(svc, dict(d)) for dom, svc, d in calls if dom == "esphome"]
    assert esphome_calls == [
        ("lounge_dropin_announce", {"room": ""}),   # kick on the ORIGIN
        ("kitchen_dropin_play", {"origin": "lounge-dot.jagbains.org"}),  # nudge the target
    ]


def test_announce_probe_seam_polled():
    """The capture-wait polls the injected probe until 200 (D-ANN-12 shape);
    a 410 ends the wait immediately (expired, hopeless)."""
    rt, _hass, _calls = make_announce_runtime()
    rt.ANN_CAPTURE_POLL_SECONDS = 0.01
    seen_urls: list[str] = []
    seq = {"n": 0}

    async def probe(url: str):
        seen_urls.append(url)
        seq["n"] += 1
        if seq["n"] < 3:
            return 404, None  # still recording
        return 200, "ann-42"

    rt._announce_probe = probe  # type: ignore[attr-defined]
    loop = asyncio.new_event_loop()
    try:
        cap = loop.run_until_complete(rt._await_capture("lounge-dot.jagbains.org"))
    finally:
        loop.close()
    assert cap == "ann-42"
    assert seen_urls and seen_urls[0] == (
        f"https://lounge-dot.jagbains.org:{DOT_ANNOUNCE_PORT}{DOT_ANNOUNCE_PATH}"
    )
    assert len(seen_urls) == 3

    # 410: one probe, stop immediately.
    async def gone(url: str):
        return 410, None
    rt._announce_probe = gone  # type: ignore[attr-defined]
    loop = asyncio.new_event_loop()
    try:
        assert loop.run_until_complete(rt._await_capture("lounge-dot.jagbains.org")) is None
    finally:
        loop.close()


def test_nudge_retry_then_skip_logged(caplog):
    """D-ANN-12: a target rejecting the nudge is retried ANN_NUDGE_ATTEMPTS
    times, then skipped WITH a log."""
    rt, _hass, calls = make_announce_runtime()
    rt.ANN_NUDGE_DELAY_SECONDS = 0.01
    attempts = {"n": 0}

    async def fail_play(device_id, action, args):
        assert action == "dropin_play"
        attempts["n"] += 1
        return {"announce_ok": False, "error": "offline"}

    rt._esphome_action = fail_play  # type: ignore[method-assign]
    with caplog.at_level("WARNING"):
        loop = asyncio.new_event_loop()
        try:
            loop.run_until_complete(rt._nudge_target(KIT, "lounge-dot.jagbains.org"))
        finally:
            loop.close()
    assert attempts["n"] == rt.ANN_NUDGE_ATTEMPTS
    assert "skipped after" in caplog.text


def test_nudge_requests_the_answer_and_names_the_lane(caplog):
    """t_98e495f0: a busy-lane answer from the dot's action response is
    surfaced, retried instead of counted as delivered, and 'accepted' is
    logged only once the lane is free."""
    rt, _hass, _calls = make_announce_runtime()
    rt.ANN_NUDGE_DELAY_SECONDS = 0.01
    answers = [("playing", {"announce": "playing"}),
               ("playing", {"announce": "playing"}),
               ("idle", {"announce": "idle"})]
    seen: list[tuple[str, str]] = []

    async def busy_then_idle(_device_id, _action, _args):
        state, res = answers[len(seen)]
        seen.append((state, res.get("announce")))
        assert res.get("announce") == state
        return dict(res, announce_ok=True)

    rt._esphome_action = busy_then_idle  # type: ignore[method-assign]
    with caplog.at_level("INFO"):
        loop = asyncio.new_event_loop()
        try:
            loop.run_until_complete(rt._nudge_target(KIT, "lounge-dot.jagbains.org"))
        finally:
            loop.close()
    assert len(seen) == 3, "busy answers retried, idle answer accepted"
    for _s, res_state in seen:
        assert res_state is not None, "lane state must reach the nudge loop (t_98e495f0)"
    assert "answered lane busy: playing" in caplog.text
    assert "accepted" in caplog.text


def test_action_call_carries_return_response():
    """t_98e495f0: the transport must request the response so the dot's
    lane-state map reaches the nudge loop (HA's esphome actions declare
    SUPPORTS_RESPONSE_OPTIONAL)."""
    rt, hass, _ = make_announce_runtime()
    seen: dict = {}

    async def spy(domain, service, data, blocking=True, **kw):
        seen.update(domain=domain, service=service,
                    blocking=blocking, return_response=kw.get("return_response"))
        return {"announce": "idle"}

    hass.async_call = spy  # type: ignore[method-assign]
    loop = asyncio.new_event_loop()
    try:
        res = loop.run_until_complete(rt._esphome_action(KIT, "dropin_play", {"origin": "x"}))
    finally:
        loop.close()
    assert seen["return_response"] is True and seen["blocking"] is True
    assert res == {"announce": "idle"}