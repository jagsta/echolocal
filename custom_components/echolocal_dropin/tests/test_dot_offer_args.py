"""D-P2P HA-side wiring tests (specs/dot-p2p-decisions.md, a55be20).

The dots negotiate their own direct pion leg (D-P2P-4); HA only supplies the
facts that make that possible, on the two actions it already invokes:

  - dropin_connect gains peer=<callee host> + transport=p2p|harelays
    (D-P2P-2/5/7). The CALLER dot is the one that receives peer — it always
    offers (D-P2P-5) — the callee gets the transport and no peer, so it only
    arms its /call/dotoffer answer gate.
  - dropin_dial gained transport too: the callee must know its transport
    before it can accept, because post-P2P no call audio may enter the dot's
    ESPHome voice turn (D-P2P-8) — including in the accept->connect window.
  - a non-dot endpoint (a phone: peer) keeps the mothballed harelays send half
    (D-P2P-1/7): the relay stays alive, unreached at the p2p default.

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
    TRANSPORT_HARELAYS,
    TRANSPORT_P2P,
)
from custom_components.echolocal_dropin.device_map import (
    AREA_TO_HOST,
    dot_host,
    session_sensor_for,
)
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
LOUNGE_HOST = "lounge-dot.jagbains.org"
KITCHEN_HOST = "kitchen-dot.jagbains.org"


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

    def get(self, entity_id: str) -> None:
        return None

    def add_job(self, *args) -> None:
        self.jobs.append(args)

    async def async_call(self, domain: str, service: str, args: dict,
                         blocking: bool = True, **kw) -> None:
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
    """Run pending add_job callbacks (the fire-and-forget dot transport).
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


def ringing_pair(rt: DropInRuntime, callee: str = KIT, caller: str = LOUNGE,
                 session_id: str = "di-p2p") -> list:
    """A caller -> callee session, past the ring floor, still RINGING."""
    clock = [1000.0]
    rt.machine.now = lambda: clock[0]
    rt.machine.call(
        session_id, Endpoint(caller, "little_lounge"), Endpoint(callee, "kitchen"),
    )
    clock[0] += RING_FLOOR_SECONDS + 1
    return clock


def calls_for(hass: FakeHass, service: str) -> list[dict]:
    return [args for _domain, svc, args, _blocking in hass.service_calls if svc == service]


# ---------------------------------------------------------------------------
# device_map: area -> hostname (D-P2P-5)
# ---------------------------------------------------------------------------


def test_dot_host_facts_and_area_mapping():
    assert dot_host(LOUNGE) == LOUNGE_HOST
    assert dot_host(KIT) == KITCHEN_HOST
    assert AREA_TO_HOST["kitchen"] == KITCHEN_HOST
    assert AREA_TO_HOST["little_lounge"] == LOUNGE_HOST


def test_dot_host_is_none_for_non_dots():
    """A phone: peer (or any unknown endpoint) has no LAN hostname — D-P2P-1
    keeps that leg off the direct path."""
    assert dot_host("phone:jag") is None
    assert dot_host("echod-kitchen") is None
    assert dot_host("") is None


# ---------------------------------------------------------------------------
# dropin_connect args (D-P2P-2/5/7)
# ---------------------------------------------------------------------------


def test_caller_gets_peer_host_and_p2p_transport():
    rt, hass, _events = make_runtime()
    clock = ringing_pair(rt)
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("in-call"), at=clock[0],
    )
    drain(hass)

    caller_args = calls_for(hass, "lounge_dropin_connect")
    assert len(caller_args) == 1
    assert caller_args[0]["peer"] == KITCHEN_HOST, "caller offers to the callee"
    assert caller_args[0]["transport"] == TRANSPORT_P2P
    assert caller_args[0]["session"] == "di-p2p"


def test_callee_gets_peerless_answer_join():
    """t_01e7db90: HA's esphome service schema makes every declared action
    arg REQUIRED, so `dropin_connect` (which declares peer) cannot be sent to
    the callee without a peer — and D-P2P-5 says the callee must never offer.
    The callee therefore receives `dropin_answer` (session+transport, no peer),
    which joins dot-side without offering."""
    rt, hass, _events = make_runtime()
    clock = ringing_pair(rt)
    rt.handle_session_sensor_change(
        KIT_SENSOR, _st("ringing"), _st("in-call"), at=clock[0],
    )
    drain(hass)

    answer_args = calls_for(hass, "kitchen_dropin_answer")
    assert len(answer_args) == 1
    assert "peer" not in answer_args[0]
    assert answer_args[0]["transport"] == TRANSPORT_P2P
    assert answer_args[0]["session"] == "di-p2p"
    # and the callee is never sent dropin_connect (its peer arg is required)
    assert calls_for(hass, "kitchen_dropin_connect") == []


def test_ring_carries_transport_so_the_turn_is_never_used_for_call_audio():
    """D-P2P-7/8: the callee's transport is known at the ring — post-P2P no
    call audio may enter the dot's ESPHome voice turn, including in the
    accept->connect window."""
    rt, hass, _events = make_runtime()
    ringing_pair(rt)
    drain(hass)

    dial_args = calls_for(hass, "kitchen_dropin_dial")
    assert len(dial_args) == 1
    assert dial_args[0]["transport"] == TRANSPORT_P2P
    assert dial_args[0]["caller"] == LOUNGE


def test_reverse_direction_swaps_the_peer_host():
    """kitchen -> lounge: the caller is the kitchen dot and the peer it offers
    to is the lounge dot (no direction assumptions baked in)."""
    rt, hass, _events = make_runtime()
    clock = ringing_pair(rt, callee=LOUNGE, caller=KIT, session_id="di-rev")
    rt.handle_session_sensor_change(
        LOUNGE_SENSOR, _st("ringing"), _st("in-call"), at=clock[0],
    )
    drain(hass)

    caller_args = calls_for(hass, "kitchen_dropin_connect")
    assert caller_args and caller_args[0]["peer"] == LOUNGE_HOST
    callee_args = calls_for(hass, "lounge_dropin_answer")
    assert callee_args and "peer" not in callee_args[0]
    assert callee_args[0]["transport"] == TRANSPORT_P2P


# ---------------------------------------------------------------------------
# non-dot endpoints keep the mothballed relay (D-P2P-1/7)
# ---------------------------------------------------------------------------


def test_phone_caller_keeps_harelays_and_no_peer():
    rt, hass, _events = make_runtime()
    clock = ringing_pair(rt, callee=KIT, caller="phone:jag", session_id="di-phone")
    rt.machine.accept("di-phone", KIT, at=clock[0])
    drain(hass)

    callee_args = calls_for(hass, "kitchen_dropin_answer")
    assert len(callee_args) == 1
    assert callee_args[0]["transport"] == TRANSPORT_HARELAYS
    assert "peer" not in callee_args[0]
    # The phone endpoint has no dot to transport to.
    assert calls_for(hass, "phone:jag_dropin_connect") == []


def test_transport_is_p2p_only_for_known_dot_pairs():
    rt, _hass, _events = make_runtime()
    assert rt._transport({"endpoints": [LOUNGE, KIT]}) == TRANSPORT_P2P
    # The ring event's shape (caller/callee, no endpoints list).
    assert rt._transport({"caller": LOUNGE, "callee": KIT}) == TRANSPORT_P2P
    assert rt._transport({"endpoints": [LOUNGE, "phone:jag"]}) == TRANSPORT_HARELAYS
    assert rt._transport({"endpoints": [LOUNGE, "echod-kitchen"]}) == TRANSPORT_HARELAYS
    assert rt._transport({"caller": "phone:jag", "callee": KIT}) == TRANSPORT_HARELAYS
    assert rt._transport({"endpoints": [LOUNGE]}) == TRANSPORT_HARELAYS
    assert rt._transport({}) == TRANSPORT_HARELAYS
