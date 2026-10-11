"""Unit tests: call state machine (D-DI-3/4/5/6) and star relay.

Run with system python per hil-framework convention (D-DI-7).
"""
from __future__ import annotations

import pytest

from custom_components.echolocal_dropin.const import (
    MAX_RECONNECT_ATTEMPTS,
    SESSION_ACTIVE,
    SESSION_ENDED,
    SESSION_RECONNECTING,
    STATE_BUSY_REJECTED,
    STATE_DIALING,
    STATE_IDLE,
    STATE_IN_CALL,
    STATE_RINGING,
)
from custom_components.echolocal_dropin.relay import RelayError, StarRelay
from custom_components.echolocal_dropin.state_machine import (
    CallError,
    CallStateMachine,
    Endpoint,
    PermissionMap,
)


class FakeClock:
    def __init__(self) -> None:
        self.t = 1000.0

    def __call__(self) -> float:
        return self.t

    def advance(self, dt: float) -> None:
        self.t += dt


class Recorder:
    def __init__(self) -> None:
        self.events: list[tuple[str, dict]] = []

    def __call__(self, kind: str, data: dict) -> None:
        self.events.append((kind, data))

    def kinds(self, kind: str) -> list[dict]:
        return [d for k, d in self.events if k == kind]


def endpoints(caller="dev-caller", callee="dev-callee", a="kitchen", b="living"):
    return Endpoint(caller, a), Endpoint(callee, b)


def make_machine(clock: FakeClock | None = None, max_call_seconds=30 * 60):
    clock = clock or FakeClock()
    rec = Recorder()
    machine = CallStateMachine(emit=rec, now=clock, max_call_seconds=max_call_seconds)
    return machine, clock, rec


# ---------------------------------------------------------------------------
# D-DI-3: permission default allow-household + per-pair map
# ---------------------------------------------------------------------------

def test_permission_default_allow_household():
    machine, _, _ = make_machine()
    assert machine.permissions.allowed("kitchen", "living") is True


def test_permission_deny_pair_blocks_call():
    machine, _, rec = make_machine()
    machine.permissions.set_pair("kitchen", "living", "deny")
    caller, callee = endpoints()
    with pytest.raises(CallError):
        machine.call("s1", caller, callee)
    assert rec.kinds("permission_denied")


def test_permission_allow_overrides_deny():
    machine, _, rec = make_machine()
    machine.permissions.set_pair("kitchen", "living", "deny")
    machine.permissions.set_pair("kitchen", "office", "allow")
    caller, callee = endpoints(callee="dev-office", b="office")
    session = machine.call("s1", caller, callee)
    assert session.state in ("ringing",)


def test_reserved_silent_connect_stored_unused():
    pm = PermissionMap()
    pm.set_pair("kitchen", "living", "allow", silent_connect=True)
    assert pm.get_pair("kitchen", "living")["silent_connect"] is True
    with pytest.raises(CallError):
        pm.set_pair("kitchen", "living", "allow", bogus_field=1)


# ---------------------------------------------------------------------------
# D-DI-3: enforced 3-second ring floor
# ---------------------------------------------------------------------------

def test_accept_before_ring_floor_refused():
    machine, clock, rec = make_machine()
    caller, callee = endpoints()
    machine.call("s1", caller, callee)
    clock.advance(1.0)
    with pytest.raises(CallError):
        machine.accept("s1", callee.device_id, at=clock())
    assert rec.kinds("accept_too_early")
    # callee remains ringing; retry after the floor succeeds
    clock.advance(2.5)  # total 3.5s >= floor
    machine.accept("s1", callee.device_id, at=clock())
    assert machine.endpoint_state(callee.device_id) == STATE_IN_CALL


def test_accept_at_exact_floor_allowed():
    machine, clock, _ = make_machine()
    caller, callee = endpoints()
    machine.call("s1", caller, callee)
    clock.advance(3.0)
    machine.accept("s1", callee.device_id, at=clock())
    assert machine.endpoint_state(callee.device_id) == STATE_IN_CALL
    assert machine.endpoint_state(caller.device_id) == STATE_IN_CALL


# ---------------------------------------------------------------------------
# D-DI-6: busy-reject
# ---------------------------------------------------------------------------

def test_busy_reject_when_callee_mid_call():
    machine, clock, rec = make_machine()
    c1, e1 = endpoints(caller="dev-a", callee="dev-b")
    s1 = machine.call("s1", c1, e1)
    clock.advance(4.0)
    machine.accept("s1", e1.device_id, at=clock())
    # third party calls dev-b
    c2, e2 = endpoints(caller="dev-c", callee="dev-b", a="office")
    with pytest.raises(CallError):
        machine.call("s2", c2, e2)
    assert rec.kinds("busy_chime")
    assert machine.endpoint_state("dev-c") == STATE_BUSY_REJECTED


def test_busy_reject_clears_on_next_call():
    machine, clock, _ = make_machine()
    c1, e1 = endpoints(caller="dev-a", callee="dev-b")
    s1 = machine.call("s1", c1, e1)
    clock.advance(4.0)
    machine.accept("s1", e1.device_id, at=clock())
    c2, e2 = endpoints(caller="dev-c", callee="dev-b", a="office")
    with pytest.raises(CallError):
        machine.call("s2", c2, e2)
    # after hangup, dev-c may call again (busy flag cleared)
    machine.hangup("s1")
    c3, e3 = endpoints(caller="dev-c", callee="dev-b", a="office")
    machine.call("s2", c3, e3)
    assert machine.endpoint_state("dev-c") != STATE_BUSY_REJECTED


def test_mutual_dial_lower_device_id_wins():
    machine, clock, rec = make_machine()
    # Both dial each other; winner is lower device-id deterministically.
    ca, ea = endpoints(caller="dev-a", callee="dev-b", a="kitchen", b="living")
    machine.call("s1", ca, ea)  # dev-a dials dev-b -> dev-b now ringing
    cb, eb = endpoints(caller="dev-b", callee="dev-a", a="living", b="kitchen")
    with pytest.raises(CallError):
        machine.call("s2", cb, eb)  # dev-b is mid-call (ringing), loses
    assert rec.kinds("busy_chime")


def test_mutual_dial_same_caller_accepts_existing():
    # Same pair dialing twice: lower device-id already owns s1; loser busy.
    machine, clock, _ = make_machine()
    c1, e1 = endpoints(caller="dev-a", callee="dev-b")
    machine.call("s1", c1, e1)
    c2, e2 = endpoints(caller="dev-a", callee="dev-b")
    with pytest.raises(CallError):
        machine.call("s2", c2, e2)


# ---------------------------------------------------------------------------
# D-DI-4: reconnect once on comeback, callee end-chime, no retry storm
# ---------------------------------------------------------------------------

def test_drop_then_single_reconnect():
    machine, clock, rec = make_machine()
    caller, callee = endpoints()
    machine.call("s1", caller, callee)
    clock.advance(4.0)
    machine.accept("s1", callee.device_id, at=clock())
    machine.drop("s1", callee.device_id)
    assert rec.kinds("end_chime")  # callee end-chime (never silence)
    assert machine.endpoint_state(callee.device_id) == STATE_IN_CALL  # reconnect window
    machine.try_reconnect("s1", callee.device_id)
    assert machine.endpoint_state(callee.device_id) == STATE_IN_CALL
    # second drop -> no more retries, session tears down
    machine.drop("s1", callee.device_id)
    assert machine.endpoint_state(callee.device_id) == STATE_IDLE
    assert rec.kinds("teardown")


def test_drop_without_pending_reconnect_then_teardown():
    machine, clock, rec = make_machine()
    caller, callee = endpoints()
    machine.call("s1", caller, callee)
    clock.advance(4.0)
    machine.accept("s1", callee.device_id, at=clock())
    # exhaust the single retry
    machine.drop("s1", callee.device_id)
    machine.try_reconnect("s1", callee.device_id)
    machine.drop("s1", callee.device_id)
    with pytest.raises(CallError):
        machine.try_reconnect("s1", callee.device_id)
    assert machine.sessions == {}


# ---------------------------------------------------------------------------
# D-DI-5: max-duration kill-switch
# ---------------------------------------------------------------------------

def test_max_duration_kill_switch():
    machine, clock, rec = make_machine(max_call_seconds=30)
    caller, callee = endpoints()
    machine.call("s1", caller, callee)
    clock.advance(4.0)
    machine.accept("s1", callee.device_id, at=clock())
    machine.gc_tick(at=clock() + 29)
    assert machine.sessions.get("s1") is not None
    machine.gc_tick(at=clock() + 31)
    assert machine.sessions == {}
    assert rec.kinds("teardown")


# ---------------------------------------------------------------------------
# End states / teardown side effects
# ---------------------------------------------------------------------------

def test_hangup_resets_both_and_emits_end_chime():
    machine, clock, rec = make_machine()
    caller, callee = endpoints()
    machine.call("s1", caller, callee)
    clock.advance(4.0)
    machine.accept("s1", callee.device_id, at=clock())
    machine.hangup("s1")
    assert machine.endpoint_state(caller.device_id) == STATE_IDLE
    assert machine.endpoint_state(callee.device_id) == STATE_IDLE
    chimes = rec.kinds("end_chime")
    assert {c["to_device"] for c in chimes} == {"dev-caller", "dev-callee"}


# ---------------------------------------------------------------------------
# Star relay (topology constraint: endpoint LIST, fan-out, specs 44dacef)
# ---------------------------------------------------------------------------

def test_relay_star_fanout_two_endpoints():
    relay = StarRelay()
    sent: list[tuple[str, bytes]] = []
    relay.bind(lambda d, f: sent.append((d, f)))
    relay.add("dev-a")
    relay.add("dev-b")
    relay.open()
    n = relay.feed("dev-a", b"\x01\x02")
    assert n == 1
    assert sent == [("dev-b", b"\x01\x02")]
    n = relay.feed("dev-b", b"\x03")
    assert n == 1
    assert sent[-1] == ("dev-a", b"\x03")


def test_relay_star_fanout_three_endpoints_additive():
    # 3-way must be additive: add a third endpoint, fan-out one-to-N.
    relay = StarRelay()
    sent: list[tuple[str, bytes]] = []
    relay.bind(lambda d, f: sent.append((d, f)))
    for d in ("dev-a", "dev-b"):
        relay.add(d)
    relay.open()
    relay.add("dev-c")
    n = relay.feed("dev-a", b"frame")
    assert n == 2
    assert [d for d, _ in sent] == ["dev-b", "dev-c"]


def test_relay_rejects_unknown_and_closed():
    relay = StarRelay()
    relay.bind(lambda d, f: None)
    relay.add("dev-a")
    with pytest.raises(RelayError):
        relay.feed("dev-a", b"x")  # closed
    relay.open()
    with pytest.raises(RelayError):
        relay.feed("dev-ghost", b"x")  # not an endpoint
    with pytest.raises(RelayError):
        relay.add("dev-a")  # duplicate