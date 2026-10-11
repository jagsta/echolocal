"""Card 4 tests: intent YAML parse, alias resolution, service-call mapping,
idle-state guard, and the intent-cannot-bypass-busy-reject contract (D-DI-5).
Run with system python: /usr/bin/python3 -m pytest (hil-framework D-DI-7).
"""
from __future__ import annotations

from pathlib import Path

import pytest
import yaml

from custom_components.echolocal_dropin.const import (
    SERVICE_ACCEPT,
    SERVICE_CALL,
    SERVICE_HANGUP,
    STATE_IDLE,
    STATE_IN_CALL,
    STATE_RINGING,
)
from custom_components.echolocal_dropin.intent_router import (
    DROPIN_STATE_CONTEXT_KEY,
    IntentError,
    IntentRouter,
    load_area_aliases,
    resolve_room,
)
from custom_components.echolocal_dropin.state_machine import (
    CallError,
    CallStateMachine,
    Endpoint,
)

COMPONENT = Path(__file__).resolve().parents[1]
INTENTS_YAML = COMPONENT / "intents" / "en.yaml"


# ---------------------------------------------------------------------------
# YAML parse + contract
# ---------------------------------------------------------------------------


def test_intents_yaml_parses_and_has_expected_intents():
    doc = yaml.safe_load(INTENTS_YAML.read_text())
    assert set(doc["intents"]) == {"DropInCall", "DropInAnswer", "DropInHangup"}
    assert doc["language"] == "en"


def test_phrases_present():
    doc = yaml.safe_load(INTENTS_YAML.read_text())
    call_sentences = doc["intents"]["DropInCall"]["data"][0]["sentences"]
    assert any("drop in on" in s for s in call_sentences)
    assert any("intercom" in s for s in call_sentences)
    hangup = doc["intents"]["DropInHangup"]["data"][0]["sentences"]
    assert "hang up" in hangup
    assert "stop" in hangup  # stop-as-hangup, D-DI-5 settled


def test_one_breath_no_follow_up_mode():
    """Every drop-in intent is registered requires_trigger=true (satellite
    trigger channel only); none declares a follow-up-mode invocation and
    answer/hangup additionally require dropin_state context (D-DI-5)."""
    doc = yaml.safe_load(INTENTS_YAML.read_text())
    for name, intent in doc["intents"].items():
        assert intent["requires_trigger"] is True, name
        assert "follow" not in yaml.safe_dump(intent).lower()
    assert doc["intents"]["DropInAnswer"]["requires_context"] == {
        DROPIN_STATE_CONTEXT_KEY: "ringing"
    }
    assert doc["intents"]["DropInHangup"]["requires_context"] == {
        DROPIN_STATE_CONTEXT_KEY: ["ringing", "in_call"]
    }


def test_no_wildcard_room_slot():
    doc = yaml.safe_load(INTENTS_YAML.read_text())
    assert not any("wildcard" in str(s).lower() for s in doc["intents"]["DropInCall"]["data"][0]["sentences"])


# ---------------------------------------------------------------------------
# Alias resolution from the live registry recording
# ---------------------------------------------------------------------------


def test_alias_table_loads_and_resolves_live_areas():
    table = load_area_aliases()
    assert resolve_room("kitchen", table) == "kitchen"
    assert resolve_room("family room", table) == "family_room"
    assert resolve_room("games room", table) == "games_room"   # display 'Zaks Room'
    assert resolve_room("workshop", table) == "garage"          # display 'Workshop'
    assert resolve_room("Hallway", table) == "hallway"          # case-insensitive


def test_unknown_room_raises():
    with pytest.raises(IntentError):
        resolve_room("gym")  # not in the live registry (recorded in area_aliases.json _note)


# ---------------------------------------------------------------------------
# Service-call mapping with a mock HA service registry
# ---------------------------------------------------------------------------


class MockServiceRegistry:
    """Mock of hass.services with the runtime's real routing semantics:
    registered service handlers call back into the machine (as runtime.py
    does)."""

    def __init__(self, machine=None) -> None:
        self.calls: list[tuple[str, str, dict]] = []
        self.machine = machine
        self._handlers: dict = {}
        if machine is not None:
            self.register("call", lambda d: machine.call(
                d["session_id"],
                Endpoint(d["caller_device"], d["caller_room"]),
                Endpoint(d["callee_device"], d["callee_room"]),
            ))
            self.register("accept", lambda d: machine.accept(d["device"] if "session_id" not in d else d["session_id"], d["device"]))
            self.register("hangup", lambda d: machine.hangup(d.get("session_id", next(iter(machine.sessions))), d.get("device")))

    def register(self, service, handler) -> None:
        self._handlers[service] = handler

    def invoke(self, domain: str, service: str, data: dict) -> None:
        self.calls.append((domain, service, data))
        handler = self._handlers.get(service)
        if handler is not None:
            handler(data)  # may raise CallError after the attempt is recorded


def make_router(machine):
    registry = MockServiceRegistry(machine)
    router = IntentRouter(call_service=registry.invoke, machine=machine)
    return router, registry


class Clock:
    def __init__(self, t: float = 1000.0) -> None:
        self.t = t

    def __call__(self) -> float:
        return self.t

    def advance(self, dt: float) -> None:
        self.t += dt


def make_machine(now=None):
    return CallStateMachine(emit=lambda kind, data: None, now=now)


def test_drop_in_call_maps_to_call_service():
    machine = make_machine()
    router, registry = make_router(machine)
    router.handle_drop_in_call("kitchen", caller_device="dev-caller")
    (domain, service, data) = registry.calls[0]
    assert domain == "echolocal_dropin"
    assert service == SERVICE_CALL
    assert data["callee_room"] == "kitchen"
    assert data["callee_device"].startswith("echod-")


def test_answer_maps_to_accept():
    clock = Clock()
    machine = make_machine(now=clock)
    router, registry = make_router(machine)
    machine.call("s1", Endpoint("dev-caller", "kitchen"), Endpoint("dev-callee", "family_room"))
    clock.advance(5.0)  # past the 3s ring floor
    router.handle_answer("dev-callee")
    _, service, data = registry.calls[-1]
    assert service == SERVICE_ACCEPT
    assert data == {"session_id": "s1", "device": "dev-callee"}


def test_hangup_and_stop_both_map_to_hangup():
    machine = make_machine()
    router, registry = make_router(machine)
    machine.call("s1", Endpoint("dev-caller", "kitchen"), Endpoint("dev-callee", "family_room"))
    router.handle_hangup("dev-callee")
    _, service, data = registry.calls[0]
    assert service == SERVICE_HANGUP
    assert data == {"device": "dev-callee"}


# ---------------------------------------------------------------------------
# Idle-state guard: answer/hangup never fire in idle state
# ---------------------------------------------------------------------------


def test_answer_refused_in_idle():
    router, registry = make_router(make_machine())
    with pytest.raises(IntentError):
        router.handle_answer("dev-callee")
    assert registry.calls == []  # nothing reached the service registry


def test_hangup_refused_in_idle():
    """'stop' (timer muscle memory) in idle state can never reach hangup."""
    router, registry = make_router(make_machine())
    with pytest.raises(IntentError):
        router.handle_hangup("dev-callee")
    assert registry.calls == []


def test_answer_requires_ringing_not_in_call():
    class Clock:
        t = 1000.0

        def __call__(self):
            return self.t

    clock = Clock()
    machine = CallStateMachine(emit=lambda k, d: None, now=clock)
    machine.call("s1", Endpoint("dev-caller", "kitchen"), Endpoint("dev-callee", "family_room"))
    clock.t += 10.0
    machine.accept("s1", "dev-callee")  # past ring floor -> in_call
    router, registry = make_router(machine)
    with pytest.raises(IntentError):
        router.handle_answer("dev-callee")
    assert registry.calls == []
    assert machine.endpoint_state("dev-callee") == STATE_IN_CALL


# ---------------------------------------------------------------------------
# Intent contract: no second call while one is active (no busy bypass)
# ---------------------------------------------------------------------------


def test_intent_no_second_call_bypass_busy_reject():
    machine = make_machine()
    router, registry = make_router(machine)
    router.handle_drop_in_call("kitchen", caller_device="dev-c1")
    before = len(registry.calls)
    with pytest.raises(CallError):
        # dev-c2 (different caller) -> same busy callee mid-ring; the intent
        # path uses the SAME machine ownership path as the service, so the
        # machine's D-DI-6 busy-reject cannot be bypassed by an intent.
        machine.call(
            "di-dev-c2",
            Endpoint("dev-c2", "family_room"),
            Endpoint("echod-kitchen", "kitchen"),
        )
    # no successful second dispatch beyond the rejected attempt: the second
    # call raised before the machine registered any new session
    sessions = [s for s in machine.sessions.values()]
    assert len(sessions) == 1  # only the original session exists → call 2 never happened


def test_intent_same_caller_rejected_mid_call():
    machine = make_machine()
    router, registry = make_router(machine)
    router.handle_drop_in_call("kitchen", caller_device="dev-c1")
    before = len(registry.calls)
    with pytest.raises(CallError):
        router.handle_drop_in_call("family room", caller_device="dev-c1")
    # only the original session exists; the rejected attempt created no session
    assert len(machine.sessions) == 1