"""Ring-timeout regression test: an unanswered ring must not wedge the caller.

Regression for the live 14 Oct failure: a RING whose transport never reached
the callee left the session RINGING forever (gc_tick only reaped ACTIVE), the
caller endpoint stayed STATE_DIALING, and every later dial was busy-rejected
until an HA restart.
"""
import sys

sys.path.insert(0, "/home/jagb/repos/echolocal/custom_components")
from echolocal_dropin.state_machine import (  # noqa: E402
    CallStateMachine,
    Endpoint,
    SESSION_ENDED,
)


def test_ring_timeout_releases_caller():
    t = [1000.0]
    m = CallStateMachine(now=lambda: t[0])
    LOUNGE = "17b8d542d93b7f00a01a79c7e9edcf60"
    KIT = "acf8f22077b452856f9ef46024f95d57"
    events = []
    m.emit = lambda k, d: events.append((k, d))

    m.call("di-one", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    assert m.endpoint_state(LOUNGE) == "dialing"

    # before the timeout the session must still be ringing
    m.gc_tick(at=t[0] + 5)
    assert "di-one" in m.sessions

    # after the timeout the session is reaped and BOTH endpoints are free
    m.gc_tick(at=t[0] + 31)
    assert "di-one" not in m.sessions
    assert m.endpoint_state(LOUNGE) == "idle"
    assert m.endpoint_state(KIT) == "idle"
    assert any(k == "teardown" and d["reason"] == "ring_timeout" for k, d in events)

    # the caller can dial again immediately
    m.call("di-two", Endpoint(LOUNGE, "little_lounge"), Endpoint(KIT, "kitchen"))
    assert "di-two" in m.sessions
    # and it too is reaped once unanswered
    m.gc_tick(at=t[0] + 100)
    assert "di-two" not in m.sessions


def test_accept_wins_over_ring_timeout():
    t = [0.0]
    m = CallStateMachine(now=lambda: t[0], ring_timeout_seconds=10)
    LOUNGE = "a"; KIT = "b"
    m.call("s", Endpoint(LOUNGE, "lr"), Endpoint(KIT, "kr"))
    t[0] = 5.0  # past the 3s ring floor, before the 10s ring timeout
    m.accept("s", KIT, at=t[0])
    assert m.endpoint_state(KIT) == "in_call"
    m.gc_tick(at=t[0] + 20)  # in-call: reaped only at max_call_seconds
    assert "s" in m.sessions


def test_ring_timeout_zero_disables():
    t = [0.0]
    m = CallStateMachine(now=lambda: t[0], ring_timeout_seconds=0)
    m.call("s", Endpoint("a", "lr"), Endpoint("b", "kr"))
    m.gc_tick(at=999999)
    assert "s" in m.sessions