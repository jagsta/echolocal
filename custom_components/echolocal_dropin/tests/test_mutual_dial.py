"""3. Mutual-dial determinism (D-DI-6).

The deterministic rule: when two endpoints dial each other, the LOWER
device-id endpoint is the session's caller and the higher-id side is
told busy — regardless of which dial arrived first. This is the
cross-instance convergence contract (both HA instances must reach the
same winner), not a timing property.

NOTE on the concurrency test: `CallStateMachine` is NOT thread-safe
(check-then-act is unsynchronized); production Home Assistant drives it
from a single-threaded asyncio loop, so the machine is effectively
serialized there. The concurrency test below is a stress harness, not
the mechanism that guarantees the rule — the guarantee is the explicit
device-id tie-break in `call()` plus the sequential
ordering-independence tests that pin it deterministically.
"""

import concurrent.futures

import pytest

from custom_components.echolocal_dropin.const import (
    RING_FLOOR_SECONDS,
    STATE_BUSY_REJECTED,
    STATE_DIALING,
    STATE_RINGING,
)
from custom_components.echolocal_dropin.state_machine import CallError, Endpoint
from custom_components.echolocal_dropin.tests.test_failure_modes import make_machine


def test_mutual_dial_concurrent_lower_device_id_wins():
    """Two CONCURRENT .call invocations (thread pool to stress ordering):
    exactly one winner (lower device-id owns the session), loser gets busy,
    and no session is left in a half-created state. Runs under BOTH
    submission orders — the result must be identical."""
    for pairs in (
        [("dev-a", "dev-b"), ("dev-b", "dev-a")],  # lower id submitted first
        [("dev-b", "dev-a"), ("dev-a", "dev-b")],  # REVERSED: higher id first
    ):
        machine, clock, rec = make_machine()

        def dial(pair):
            caller, callee = pair
            try:
                s = machine.call("s-x", Endpoint(caller, "kitchen"), Endpoint(callee, "living"))
                return ("ok", s.session_id)
            except CallError as err:
                return ("busy", str(err))

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(dial, pairs))

        # Exactly one live session remains; its caller is the LOWER device-id
        # (dev-a) under BOTH submission orders — that is the D-DI-6 contract.
        # (In reversed order dev-b's dial can return ok because dev-a's dial
        # re-owns the session; ownership, not the raise, is the contract.)
        assert len(machine.sessions) == 1, pairs
        winner_sid = next(iter(machine.sessions))
        session = machine.sessions[winner_sid]
        assert session.endpoints[0].device_id == "dev-a", pairs
        assert machine.endpoint_state("dev-b") == STATE_RINGING
        assert machine.endpoint_state("dev-a") == STATE_DIALING
        # The final ring (if any superseding one was emitted) must name
        # dev-a as caller; the authoritative state is the contract, not
        # the count of interim events.
        rings = rec.kinds("ring")
        if rings:
            assert rings[-1]["caller"] == "dev-a", (pairs, rings)
        # re-own event only fires when the higher-id dial arrives first
        assert len(rec.kinds("mutual_dial_reowned")) == (1 if pairs[0][0] == "dev-b" else 0)


def test_mutual_dial_ordering_independent_winner():
    """Invoke both dials in either sequential order: lower device-id (dev-a)
    always ends as the session owner; the dev-b side is busy-rejected.
    When dev-b dials first, dev-a's dial RE-OWNS the session so the lower
    id becomes the caller (D-DI-6 deterministic tie-break)."""
    # Order 1: dev-a (lower) dials first, dev-b's counter-dial is rejected.
    machine, clock, rec = make_machine()
    machine.call("s1", Endpoint("dev-a", "kitchen"), Endpoint("dev-b", "living"))
    with pytest.raises(CallError):
        machine.call("s2", Endpoint("dev-b", "living"), Endpoint("dev-a", "kitchen"))
    assert machine.endpoint_state("dev-b") == STATE_RINGING
    assert machine.sessions["s1"].endpoints[0].device_id == "dev-a"

    # Order 2: dev-b (higher) dials first, then dev-a dials back -> dev-a
    # re-owns the session; dev-b is busy-chimed. Session is re-keyed to
    # dev-a's session id.
    machine2, clock2, rec2 = make_machine()
    machine2.call("s1", Endpoint("dev-b", "living"), Endpoint("dev-a", "kitchen"))
    assert machine2.sessions["s1"].endpoints[0].device_id == "dev-b"  # interim owner
    machine2.call("s2", Endpoint("dev-a", "kitchen"), Endpoint("dev-b", "living"))
    assert "s1" not in machine2.sessions  # re-keyed away from dev-b's id
    assert machine2.sessions["s2"].endpoints[0].device_id == "dev-a"
    assert machine2.endpoint_state("dev-a") == STATE_DIALING
    assert machine2.endpoint_state("dev-b") == STATE_RINGING
    busy_chimes = rec2.kinds("busy_chime")
    assert busy_chimes and busy_chimes[-1]["to_device"] == "dev-b"
    assert rec2.kinds("mutual_dial_reowned")


def test_mutual_dial_single_session_after_reown_and_accept():
    """After a re-own, the merged session is the ONLY session and behaves
    normally: floor-gated accept connects both endpoints."""
    machine, clock, _ = make_machine()
    machine.call("s1", Endpoint("dev-b", "living"), Endpoint("dev-a", "kitchen"))
    machine.call("s2", Endpoint("dev-a", "kitchen"), Endpoint("dev-b", "living"))
    assert list(machine.sessions) == ["s2"]
    clock.advance(RING_FLOOR_SECONDS)
    machine.accept("s2", "dev-b", at=clock())
    assert machine.sessions["s2"].state == "active"