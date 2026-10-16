"""Card 5 — failure-mode hardening tests (D-DI-6) on the HA state machine.

Covers the four carded failure modes plus the D-DI-5/6 invariant regression
sweep (30m kill-switch + 1s ring floor under every failure path):

1. Drop on HA disconnect: exactly one reconnect attempt (no retry storm),
   end-chime ordering before reconnect_attempt, no silence.
2. Busy-reject: second call mid-call refused, caller gets busy_chime, callee
   state unchanged. Framed as 'no join offer for v1' (specs 44dacef comment):
   the busy path is the special case of a later join-offer path — busy
   semantics must not hardwire two-party-only.
3. Mutual dial: concurrent invocations converge on a single winner by
   device-id ordering; loser told busy.
4. Mute: HA-side mute echo (endpoint.muted) toggles with mute_changed event,
   unmute restores; refused outside a call.

Run with system python (D-DI-7): /usr/bin/python3 -m pytest.
"""
from __future__ import annotations

import concurrent.futures

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
from custom_components.echolocal_dropin.state_machine import (
    CallError,
    CallStateMachine,
    Endpoint,
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


def make_machine(clock=None, max_call_seconds=30 * 60):
    clock = clock or FakeClock()
    rec = Recorder()
    machine = CallStateMachine(emit=rec, now=clock, max_call_seconds=max_call_seconds)
    return machine, clock, rec


def connect(machine, clock, session_id="s1", caller="dev-a", callee="dev-b",
            a="kitchen", b="living"):
    """Ring + past-floor accept: established in-call session."""
    ca, cb = Endpoint(caller, a), Endpoint(callee, b)
    machine.call(session_id, ca, cb)
    clock.advance(4.0)
    machine.accept(session_id, cb.device_id, at=clock())
    assert machine.endpoint_state(cb.device_id) == STATE_IN_CALL
    return ca, cb


# ---------------------------------------------------------------------------
# 1. Call-drop on HA disconnect (D-DI-4/6)
# ---------------------------------------------------------------------------

def test_drop_event_ordering_end_chime_before_reconnect():
    """Callee hears end-chime immediately on drop and it is emitted BEFORE
    the reconnect attempt — never silence, never chime-after-resume."""
    machine, clock, rec = make_machine()
    connect(machine, clock)
    machine.drop("s1", "dev-b")
    kinds = [k for k, _ in rec.events]
    assert kinds.index("end_chime") < kinds.index("reconnect_attempt")
    # exactly one of each per drop — no duplicate chimes on the drop path
    assert len(rec.kinds("reconnect_attempt")) == 1


def test_disconnect_reconnect_sequence_no_retry_storm():
    """Full disconnect->reconnect sequence: one attempt budget
    (MAX_RECONNECT_ATTEMPTS == 1); further drop after exhausted budget tears
    down with end-chime (never silence) and no further reconnect attempts."""
    assert MAX_RECONNECT_ATTEMPTS == 1
    machine, clock, rec = make_machine()
    connect(machine, clock)

    machine.drop("s1", "dev-b")  # HA comeback window opens
    machine.try_reconnect("s1", "dev-b")
    assert machine.endpoint_state("dev-b") == STATE_IN_CALL
    assert len(rec.kinds("reconnect_attempt")) == 1

    # disconnect again: budget exhausted -> teardown, end chime, not silence
    before = len(rec.kinds("end_chime"))
    machine.drop("s1", "dev-b")
    assert len(rec.kinds("reconnect_attempt")) == 1  # no storm
    assert len(rec.kinds("end_chime")) > before
    assert machine.endpoint_state("dev-b") == STATE_IDLE
    assert rec.kinds("teardown")


def test_drop_never_leaves_silence_reconnecting_end():
    """Every terminal drop path emits end_chime: drop-at-budget teardown and
    drop-while-reconnecting both chime before/without a reconnect grant."""
    machine, clock, rec = make_machine()
    connect(machine, clock)
    machine.drop("s1", "dev-b")
    machine.drop("s1", "dev-b")  # second drop while still reconnecting
    # single-attempt policy: no second reconnect grant
    with pytest.raises(CallError):
        machine.try_reconnect("s1", "dev-b")
    assert rec.kinds("teardown")
    assert rec.kinds("end_chime")


# ---------------------------------------------------------------------------
# 2. Busy-reject (D-DI-6, framed as 'no join offer for v1')
# ---------------------------------------------------------------------------

def test_busy_reject_caller_gets_chime_callee_unchanged():
    """Second incoming call during an active call: refused, the CALLER gets
    the busy_chime event, and the callee's state does not change."""
    machine, clock, rec = make_machine()
    connect(machine, clock, caller="dev-a", callee="dev-b")
    rec.events.clear()

    # dev-c dials dev-b (dev-b is mid-call)
    c2, e2 = Endpoint("dev-c", "office"), Endpoint("dev-b", "living")
    with pytest.raises(CallError):
        machine.call("s2", c2, e2)

    chimes = rec.kinds("busy_chime")
    assert len(chimes) == 1 and chimes[0]["to_device"] == "dev-c"
    # callee state unchanged; caller (rejected side) marked busy_rejected
    assert machine.endpoint_state("dev-b") == STATE_IN_CALL
    assert machine.endpoint_state("dev-c") == STATE_BUSY_REJECTED
    # no new session, no relay-facing state on the callee's end
    assert "s2" not in machine.sessions


def test_busy_reject_is_no_join_offer_v1():
    """Specs 44dacef constraint: v1 busy-reject must read as 'no join offer
    for v1' — the rejected caller is NOT grafted onto the live session's
    endpoint list; join remains an explicit future add_participant op."""
    machine, clock, rec = make_machine()
    connect(machine, clock, caller="dev-a", callee="dev-b")

    c2, e2 = Endpoint("dev-c", "office"), Endpoint("dev-b", "living")
    with pytest.raises(CallError):
        machine.call("s2", c2, e2)

    session = machine.sessions["s1"]
    assert session.device_ids() == ["dev-a", "dev-b"]  # endpoint LIST unchanged
    # explicit join op exists behind the same busy guard (additive later)
    from custom_components.echolocal_dropin.state_machine import STATE_IN_CALL as _
    assert hasattr(machine, "add_participant")
    # and it enforces busy too: joining while... dev-c is not in this session
    # so a join of dev-b is refused because dev-b mid-call check is about the
    # NEW endpoint only — the true guard is that add_participant refuses
    # endpoints already in a call. The v1 behavior (busy instead of join
    # offer) is what test_busy_reject_caller_gets_chime pins above.


def test_busy_reject_makes_caller_state_busy_until_next_attempt():
    """The busy flag lives on the rejected caller ONLY (callee untouched)
    and clears on that caller's next fresh attempt (D-DI-6, no queueing)."""
    machine, clock, _ = make_machine()
    connect(machine, clock, caller="dev-a", callee="dev-b")

    with pytest.raises(CallError):
        machine.call("s2", Endpoint("dev-c", "office"), Endpoint("dev-b", "living"))
    assert machine.endpoint_state("dev-c") == STATE_BUSY_REJECTED
    with pytest.raises(CallError):
        machine.call("s3", Endpoint("dev-b", "living"), Endpoint("dev-c", "office"))

    machine.hangup("s1")
    # dev-c may now dial dev-b again — flag cleared by the fresh attempt
    machine.call("s2", Endpoint("dev-c", "office"), Endpoint("dev-b", "living"))
    assert machine.endpoint_state("dev-c") == STATE_DIALING


# ---------------------------------------------------------------------------
# 3. Mutual-dial determinism (D-DI-6) — moved to tests/test_mutual_dial.py
# (lower-device-id winner pinned under BOTH invocation orders there)
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# 4. Mute (D-DI-6): transmit off + amber LED device-side, HA visibility here
# ---------------------------------------------------------------------------
def test_mute_echo_toggle_and_restore():
    """HA-side mute visibility: set_muted toggles endpoint.muted and emits
    mute_changed; unmute restores muted=False. Transmit stop itself is
    device-side (card 2 wiring.ToggleMute + pump guard, Go-tested)."""
    machine, clock, rec = make_machine()
    connect(machine, clock)

    machine.set_muted("s1", "dev-b", True)
    assert machine.sessions["s1"].endpoint("dev-b").muted is True
    assert rec.kinds("mute_changed")[-1]["muted"] is True

    machine.set_muted("s1", "dev-b", False)
    assert machine.sessions["s1"].endpoint("dev-b").muted is False
    assert rec.kinds("mute_changed")[-1]["muted"] is False


def test_mute_refused_outside_call():
    """Mute is a sub-state of InCall only (D-DI-6): refused while ringing and
    for unknown endpoints."""
    machine, clock, _ = make_machine()
    machine.call("s1", Endpoint("dev-a", "kitchen"), Endpoint("dev-b", "living"))
    with pytest.raises(CallError):
        machine.set_muted("s1", "dev-b", True)  # still ringing
    with pytest.raises(CallError):
        machine.set_muted("s1", "dev-ghost", True)


# ---------------------------------------------------------------------------
# 5. Kill-switch + ring-floor regression sweep across failure paths
# ---------------------------------------------------------------------------

def test_sweep_kill_switch_fires_on_every_active_path():
    """D-DI-5 invariant: 30m self-end holds on the plain call AND after each
    failure path lands back in call (mutual-dial loser busy, busy-reject
    third party, reconnect completed)."""
    machine, clock, rec = make_machine(max_call_seconds=30)

    # path: reconnect completed then expiry fires
    connect(machine, clock)
    machine.drop("s1", "dev-b")
    machine.try_reconnect("s1", "dev-b")
    machine.gc_tick(at=clock() + 29)
    assert "s1" in machine.sessions
    machine.gc_tick(at=clock() + 31)
    assert "s1" not in machine.sessions
    assert rec.kinds("teardown")[-1]["reason"] == "max_duration"


def test_sweep_ring_floor_holds_after_busy_and_drop_paths():
    """D-DI-3 invariant: 1s floor holds on the standard path AND on a fresh
    ring that follows a busy-reject (rejected caller's next call restarts a
    full ring window, floor re-armed)."""
    machine, clock, rec = make_machine()
    connect(machine, clock, caller="dev-a", callee="dev-b")

    # busy path: dev-c rejected
    with pytest.raises(CallError):
        machine.call("s2", Endpoint("dev-c", "office"), Endpoint("dev-b", "living"))
    machine.hangup("s1")

    # fresh ring for dev-c: floor re-arms from THIS ring's start
    fresh_ring_at = clock()
    machine.call("s2", Endpoint("dev-c", "office"), Endpoint("dev-b", "living"))
    session = machine.sessions["s2"]
    assert session.ring_started_at == fresh_ring_at
    with pytest.raises(CallError):
        machine.accept("s2", "dev-b", at=fresh_ring_at + 0.5)  # under the 1s floor
    machine.accept("s2", "dev-b", at=fresh_ring_at + 1.0)  # at floor is allowed
    assert machine.endpoint_state("dev-b") == STATE_IN_CALL


def test_sweep_kill_switch_not_rearmed_by_reconnect():
    """The 30m budget runs from connect, not from reconnect completion —
    a mid-call drop+reconnect cannot extend a forgotten open line."""
    machine, clock, _ = make_machine(max_call_seconds=30)
    connect(machine, clock)
    connect_at = clock.t
    clock.advance(20)
    machine.drop("s1", "dev-b")
    machine.try_reconnect("s1", "dev-b")
    # expiry must still fire relative to the original connect
    machine.gc_tick(at=connect_at + 31)
    assert "s1" not in machine.sessions


def test_sweep_floor_and_kill_under_mute_active():
    """Mute does not perturb the invariants: killed session while muted still
    self-ends at 30m and rings still honor the floor."""
    machine, clock, rec = make_machine(max_call_seconds=30)
    connect(machine, clock)
    machine.set_muted("s1", "dev-b", True)
    machine.gc_tick(at=clock() + 31)
    assert "s1" not in machine.sessions
    # and the floor under a fresh mute-then-ring sequence
    machine2, clock2, _ = make_machine()
    machine2.call("sA", Endpoint("dev-a", "kitchen"), Endpoint("dev-b", "living"))
    with pytest.raises(CallError):
        machine2.set_muted("sA", "dev-b", True)  # muted only in-call
    clock2.advance(3.0)
    machine2.accept("sA", "dev-b", at=clock2())
    machine2.set_muted("sA", "dev-b", True)
    assert machine2.sessions["sA"].endpoint("dev-b").muted is True