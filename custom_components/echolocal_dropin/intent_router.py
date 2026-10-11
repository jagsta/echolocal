"""Intent -> service call mapping for the EchoLocal drop-in intercom (D-DI-5).

Card 4: translates Assist conversation-intent matches (intents/en.yaml) into
echolocal_dropin service calls, behind three guards:

1. Alias resolution: the spoken ``room`` slot resolves through
   area_aliases.json (recorded from Jag's live HA area registry) onto the
   authoritative ``area_id`` used as the call target. Unknown rooms raise
   IntentError, so the busy/invalid path is explicit instead of a silent
   wrong-room call.
2. Idle-state guard on answer/hangup: the intent YAML carries
   ``requires_context: dropin_state``; this module RE-asserts it against the
   session state machine so a YAML-only match in idle state can never reach
   the services (defense in depth — the "stop"/timer ambiguity accepted in
   D-DI-5 is bounded by this).
3. Busy/no-bypass guard: a second DropInCall while any endpoint is
   ringing/dialing/in-call is left to the state machine's busy-reject and is
   asserted (test) to raise CallError through the intent path — intents use
   the same machine; no intent bypass exists.

Pure-Python and unit-testable without Home Assistant (D-DI-7): the caller
in HA passes a mock/fake service registry; tests inject a fake machine.
"""
from __future__ import annotations

import json
from pathlib import Path
from typing import Callable

from .const import (
    DOMAIN,
    SERVICE_ACCEPT,
    SERVICE_CALL,
    SERVICE_HANGUP,
    STATE_IDLE,
    STATE_RINGING,
)
from .state_machine import CallError

ALIASES_PATH = Path(__file__).parent / "area_aliases.json"

# The satellite exposes its drop-in state as conversation context under this
# key; the intent YAML requires it for answer/hangup (idle-state guard).
DROPIN_STATE_CONTEXT_KEY = "dropin_state"


class IntentError(Exception):
    """Raised when an intent match cannot be translated into a service call."""


def load_area_aliases(path: Path = ALIASES_PATH) -> dict[str, str]:
    """spoken alias (normalized) -> area_id, from area_aliases.json."""
    data = json.loads(path.read_text())
    table: dict[str, str] = {}
    for area in data["areas"]:
        area_id = area["area_id"]
        names = [area["name"], area_id.replace("_", " ")]
        names += area.get("spoken_aliases", [])
        for name in names:
            table.setdefault(_normalize(name), area_id)
    return table


def _normalize(text: str) -> str:
    return " ".join(text.strip().lower().replace("'", "").split())


def resolve_room(spoken: str, aliases: dict[str, str] | None = None) -> str:
    """Resolve a spoken room name to the HA area_id (target for the call)."""
    table = aliases if aliases is not None else load_area_aliases()
    key = _normalize(spoken)
    area_id = table.get(key)
    if area_id is None:
        raise IntentError(f"unknown room: {spoken!r} (no alias in area_aliases.json)")
    return area_id


class IntentRouter:
    """Maps matched intents to echolocal_dropin service calls.

    ``call_service`` is a callable (domain, service, data) -> None so tests
    inject a mock HA service registry; the runtime wires the real
    hass.services.async_call in production.
    """

    def __init__(
        self,
        call_service: Callable[[str, str, dict], None],
        machine,  # CallStateMachine, for state guards
        aliases: dict[str, str] | None = None,
    ) -> None:
        self._call_service = call_service
        self._machine = machine
        self._aliases = aliases

    # -- caller side --------------------------------------------------------

    def handle_drop_in_call(self, room: str, caller_device: str) -> None:
        area_id = resolve_room(room, self._aliases)
        session_id = f"di-{caller_device}"
        # Busy-reject (D-DI-6) is enforced BY the machine below; an intent
        # cannot bypass it because this is the same call-ownership path the
        # service uses (test_intent_no_second_call_bypass proves CallError).
        self._call_service(
            DOMAIN,
            SERVICE_CALL,
            {
                "caller_device": caller_device,
                "caller_room": self._caller_room(caller_device),
                "callee_device": self._callee_device(area_id),
                "callee_room": area_id,
                "session_id": session_id,
            },
        )

    def _caller_room(self, caller_device: str) -> str:
        for session in self._machine.sessions.values():
            ep = session.endpoint(caller_device)
            if ep is not None:
                return ep.room
        return "unknown"

    def _callee_device(self, area_id: str) -> str:
        return f"echod-{area_id}"

    # -- callee side --------------------------------------------------------

    def handle_answer(self, device_id: str) -> None:
        self._require_call_session(device_id, expected=STATE_RINGING, intent="answer")
        session_id = self._session_id_for(device_id)
        self._call_service(DOMAIN, SERVICE_ACCEPT, {"session_id": session_id, "device": device_id})

    def handle_hangup(self, device_id: str) -> None:
        self._require_call_session(device_id, expected=None, intent="hang up/stop")
        self._call_service(DOMAIN, SERVICE_HANGUP, {"device": device_id})

    # -- guards ---------------------------------------------------------------

    def _session_id_for(self, device_id: str) -> str:
        for sid, session in self._machine.sessions.items():
            if session.endpoint(device_id) is not None:
                return sid
        raise IntentError(f"no session contains {device_id}")

    def _require_call_session(self, device_id: str, *, expected, intent: str) -> None:
        """Idle-state guard: no answer / hang up when idle; reasserts the
        intent YAML's requires_context: dropin_state in code."""
        state = self._machine.endpoint_state(device_id)
        if state == STATE_IDLE:
            raise IntentError(f"'{intent}' refused in idle state (no active drop-in)")
        if expected is not None and state != STATE_RINGING:
            raise IntentError(f"'{intent}' requires ringing state, got {state!r}")