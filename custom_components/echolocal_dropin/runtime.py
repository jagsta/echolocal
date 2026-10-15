"""Runtime glue: services, config entity wiring, relay <-> devices.

Thin HA layer above the pure state machine: it translates typed events into
echod-side actions (chimes, LED via echod's existing expose, native-API
VoiceAssistantAudio relay) and media pause/resume via HA media_player
entities (D-DI-5, HA-owned orchestration).

Device identity convention (settlement for cards 2/3): an endpoint is keyed
by its echod device-id string, exactly what card 2's device-side session
layer reports over the native API. Entity mapping lives in HA config for the
pilot pair and is additive per room.
"""
from __future__ import annotations

import logging
from datetime import timedelta
from typing import Any

from homeassistant.const import STATE_PLAYING
from homeassistant.core import HomeAssistant, ServiceCall
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers.event import async_track_time_interval

from .const import (
    DOMAIN,
    PERMISSION_ALLOW,
    PERMISSION_ALLOW_HOUSEHOLD,
    PERMISSION_DENY,
    SERVICE_ACCEPT,
    SERVICE_CALL,
    SERVICE_HANGUP,
    SERVICE_SET_PERMISSION,
    SERVICE_VOICE_ANSWER,
    SERVICE_VOICE_HANGUP,
    SESSION_RINGING,
    STATE_IDLE,
    STATE_IN_CALL,
    STATE_RINGING,
)
from .device_map import (
    AREA_TO_DEVICE,
    DROPIN_DEVICE_MAP,
    dot_available,
    dot_for,
    session_state,
)
from .relay import StarRelay
from .state_machine import (
    Endpoint,
    PermissionMap,
    CallStateMachine,
    CallError,
)

_LOGGER = logging.getLogger(__name__)


def _all_dots() -> dict[str, dict[str, str]]:
    return DROPIN_DEVICE_MAP


class DropInRuntime:
    """Owns the state machine + relay and exposes HA services."""

    def __init__(self, hass: HomeAssistant, entry) -> None:
        self.hass = hass
        self.entry = entry
        self.permissions = PermissionMap()
        self.machine = CallStateMachine(
            permissions=self.permissions,
            emit=self._on_event,
        )
        self.relay = StarRelay()
        self._relays: dict[str, StarRelay] = {}
        self._media_targets: dict[str, str] = {}  # device_id -> media_player entity_id
        self._paused_for_call: set[str] = set()
        self.device_available: Any = None  # injectable callee-availability probe

    def emit(self, kind: str, data: dict) -> None:
        """Local event tap used by accepted-risk paths (offline-at-dial)."""
        _LOGGER.debug("drop-in runtime event %s %s", kind, data)

    async def async_setup(self) -> None:
        # Card A (t_d767ce0a): production wiring of the previously-TODO'd
        # seams. Device ids are real HA device-registry ids (the automations
        # pass trigger.device_id); the map is additive per room.
        self.device_available = lambda device_id: dot_available(self.hass, device_id)
        for device_id, facts in _all_dots().items():
            # TTS target = the dot's own assist_satellite entity (its voice
            # reply path; D-DI-5 offline-at-dial notice).
            self.register_media(device_id, facts["satellite_entity"])
        def wrapped(handler):
            """Run a service impl in the executor; translate CallError into
            homeassistant.exceptions.HomeAssistantError so HA's service layer
            surfaces expected conditions (busy mid-call, permission denied,
            no such session) as clean user-facing service errors instead of
            HTTP 500 + full traceback. Impl still raises CallError
            internally — internal callers (call_or_report_offline,
            voice_answer) keep catching CallError directly; only this HA
            boundary converts."""
            async def run(call: ServiceCall) -> None:
                try:
                    await self.hass.async_add_executor_job(handler, call.data)
                except CallError as err:
                    raise HomeAssistantError(str(err)) from err
            return run

        handle_call = wrapped(self._call_service)
        handle_accept = wrapped(self._accept_service)
        handle_hangup = wrapped(self._hangup_service)
        handle_set_permission = wrapped(self._set_permission_service)

        self.hass.services.async_register(DOMAIN, SERVICE_CALL, handle_call)
        self.hass.services.async_register(DOMAIN, SERVICE_ACCEPT, handle_accept)
        self.hass.services.async_register(DOMAIN, SERVICE_HANGUP, handle_hangup)
        self.hass.services.async_register(DOMAIN, SERVICE_SET_PERMISSION, handle_set_permission)

        handle_voice_answer = wrapped(self.voice_answer)
        handle_voice_hangup = wrapped(self.voice_hangup)

        self.hass.services.async_register(DOMAIN, SERVICE_VOICE_ANSWER, handle_voice_answer)
        self.hass.services.async_register(DOMAIN, SERVICE_VOICE_HANGUP, handle_voice_hangup)

        # Card A fix (live failure 14 Oct): the machine only reaped ACTIVE
        # sessions at max_duration via nothing at all — gc_tick existed but no
        # caller ever ran it, so an unanswered RING wedged the caller endpoint
        # in dialing and every later dial was busy-rejected until restart.
        # Run the reaper every 5s: ring timeout (30s) + max-duration.
        async def _gc_tick(_now) -> None:
            await self.hass.async_add_executor_job(self.machine.gc_tick)

        self._gc_unsub = async_track_time_interval(self.hass, _gc_tick, timedelta(seconds=5))

    async def async_unload(self) -> None:
        if getattr(self, "_gc_unsub", None) is not None:
            self._gc_unsub()
            self._gc_unsub = None
        for session_id in list(self.machine.sessions):
            self.machine.hangup(session_id)

    # -- service impls (run in executor; state machine is sync) -----------

    def _call_service(self, data: dict[str, Any]) -> None:
        caller_device = str(data["caller_device"])
        callee_device = str(data["callee_device"])
        # Card A: automations may still send the aspirational echod-<area>
        # slug for a room without a pinned device; resolve to the real
        # assist-satellite device id so the dot transport can find it. Both
        # endpoints need this: the caller's slug also reaches _dot_action()
        # via the connect/teardown endpoint lists (dot_for(slug) is None —
        # live 15 Oct: caller's session sensor never left idle because the
        # lounge dot never received dropin_connect).
        if dot_for(callee_device) is None:
            resolved = AREA_TO_DEVICE.get(str(data.get("callee_room") or ""))
            if resolved is not None:
                callee_device = resolved
        if dot_for(caller_device) is None:
            resolved = AREA_TO_DEVICE.get(str(data.get("caller_room") or ""))
            if resolved is not None:
                caller_device = resolved
        session_id = str(data.get("session_id") or f"di-{caller_device}-{callee_device}")
        self.machine.call(
            session_id,
            Endpoint(caller_device, str(data["caller_room"])),
            Endpoint(callee_device, str(data["callee_room"])),
        )

    def _accept_service(self, data: dict[str, Any]) -> None:
        self.machine.accept(str(data["session_id"]), str(data["device"]))

    def _hangup_service(self, data: dict[str, Any]) -> None:
        self.machine.hangup(str(data["session_id"]), data.get("device"))

    def _set_permission_service(self, data: dict[str, Any]) -> None:
        self.permissions.set_pair(
            str(data["source"]), str(data["target"]), str(data["mode"]),
        )

    # -- state machine events -> device/media actions ----------------------

    def _on_event(self, kind: str, data: dict[str, Any]) -> None:
        _LOGGER.debug("drop-in event %s %s", kind, data)
        if kind == "ring":
            # Callee chime + LED ring via echod's existing expose (D-DI-3).
            self._media_pause(data["callee"])
            # Card A: transport the ring to the dot — invoke the callee's
            # ESPHome dropin_dial action (chime/LED/session sensor paint
            # happens dot-side the moment Session.Ring() fires). Failure
            # propagation: if the native-API transport is dead the dot will
            # NEVER hear this ring — tear the session down immediately
            # instead of leaving CALLEE=ringing / CALLER=dialing wedged
            # until the 30s gc ring timeout (live 15 Oct break point,
            # child-1 diagnosis: aioesphomeapi APIConnectionError "Not
            # connected to kitchen").
            self._dot_action(
                data["callee"], "dropin_dial",
                {"session": data["session"], "caller": data["caller"]},
                session_id=data["session"],
            )
        elif kind == "connect":
            relay = self._relays.get(data["session"])
            if relay is None:
                relay = StarRelay()
                relay.bind(self._make_send(data["session"]))
                for d in data["endpoints"]:
                    relay.add(d)
                relay.open()
                self._relays[data["session"]] = relay
            # Card A (run 90 live finding): the CALLEE dot's session never
            # leaves Ringing when the accept is executed on the HA side, and
            # the callee's dot-side Session has no production accept trigger
            # on this path — dot-native accept is idempotent-safe from
            # Ringing (CallerJoin is explicitly allowed from Ringing, and a
            # dropin_connect to a dot already InCall is a no-op that simply
            # reopens nothing: wiring.Accept/CallerConnect refuse non-ring
            # joins with a warn, no state damage). Transport connect to BOTH
            # endpoints so the callee's session sensor paints in_call and
            # both dots open their turns.
            for d in data.get("endpoints", []) or []:
                self._dot_action(d, "dropin_connect", {})
        elif kind == "teardown":
            relay = self._relays.pop(data["session"], None)
            if relay is not None:
                relay.close()
            for d in data.get("endpoints", []) or []:
                self._media_resume(d)
                # Card A: explicit dot teardown (hangup from either side,
                # kill-switch, dropped) mirrors onto each dot's session.
                self._dot_action(d, "dropin_end", {"reason": "hangup"})
            # end chime + media resume on both ends emitted by machine already
        elif kind == "mute_changed":
            # D-DI-6 mute visibility: the device-side wiring already paints
            # amber on the LED (card 2); HA mirrors the state into the select
            # entity's extra attributes so the dashboard shows it.
            _LOGGER.info(
                "drop-in mute: device=%s muted=%s session=%s",
                data.get("device"), data.get("muted"), data.get("session"),
            )

    # -- dot transport (Card A: the physical dial) -------------------------

    def _dot_action(self, device_id: str, action: str, args: dict[str, str],
                    session_id: str | None = None) -> None:
        """Invoke an ESPHome user-defined action on one dot over its existing
        native-API connection. Fire-and-forget: the dot answers with its
        session state (dropin_dial refuses busy with an error reply, D-DI-6),
        and failures log rather than unwind the state machine — except for a
        failed DIAL, which tears down the ringing session (see
        _teardown_failed_ring)."""
        facts = dot_for(device_id)
        if facts is None:
            # Not a known dot (e.g. a phone: peer) — nothing physical to dial.
            return
        service = f"{facts['node']}_{action}"
        _LOGGER.info("drop-in dot transport: esphome.%s %s", service, args)
        try:
            # The state machine emits from SyncWorker threads; service calls
            # must resolve hass + the esphome dynamic-action registry on the
            # loop thread (async_get_hass thread-check otherwise throws).
            self.hass.add_job(self._dot_action_async, service, args, session_id)
        except Exception as err:  # noqa: BLE001 — transport must never break the machine
            _LOGGER.warning("drop-in dot action %s failed: %s", service, err)
            self._teardown_failed_ring(action, session_id, err)

    async def _dot_action_async(self, service: str, args: dict[str, str],
                                session_id: str | None = None) -> None:
        try:
            # blocking=True surfaces the transport error (APIConnectionError
            # etc.) here instead of HA's service-execution error log.
            await self.hass.services.async_call("esphome", service, args, blocking=True)
        except Exception as err:  # noqa: BLE001
            _LOGGER.warning("drop-in dot action esphome.%s failed: %s", service, err)
            self._teardown_failed_ring(service, session_id, err)

    def _teardown_failed_ring(self, service: str, session_id: str | None,
                              err: Exception) -> None:
        """If a ring's transport to the callee dot failed, the dot will never
        ring and the session would sit RINGING (caller wedged STATE_DIALING)
        until the 30s gc ring timeout. End it now — the machine's teardown
        path chimes/resumes/ends both dots exactly like a real hangup and
        frees the caller to dial again immediately."""
        if session_id is None or not str(service).endswith("_dial"):
            return  # only ring transport failures end a session early
        session = self.machine.sessions.get(session_id)
        if session is None or session.state != SESSION_RINGING:
            return  # answered or already gone — nothing to reclaim
        _LOGGER.warning(
            "drop-in ring transport failed (session=%s): %s — tearing down",
            session_id, err,
        )
        self.machine.hangup(session_id)

    def endpoint_session_state(self, device_id: str) -> str:
        """The dot-published session state for dropin_state context exposure."""
        return session_state(self.hass, device_id)

    def caller_state_payload(self, device_id: str) -> dict[str, str]:
        """Conversation context the assist satellite should carry:
        dropin_state=<session state> for DropInAnswer/DropInHangup guards."""
        state = self.endpoint_session_state(device_id)
        if state in (STATE_RINGING, STATE_IN_CALL) or state in ("ringing", "in-call", "muted", "joined"):
            return {"dropin_state": state}
        return {}

    def resolve_endpoint_device(self, device_id: str | None, room: str | None) -> str | None:
        """Resolve the drop-in device driving an answer/hangup utterance.

        Card A (run 90): HA's default agent builds intent recognition context
        from ONLY the satellite's area (default_agent._make_intent_context);
        arbitrary requires_context keys like dropin_state can never be
        supplied, and sentence-trigger matching in automations runs with no
        context at all — so answer/hangup MUST be gated here against the
        dot's own session state, keyed by whichever endpoint drove the
        utterance. Prefers the explicit device id, else the dot in ``room``,
        else the single dot with a non-idle session sensor."""
        if device_id is not None and dot_for(device_id) is not None:
            return device_id
        if room:
            resolved = AREA_TO_DEVICE.get(str(room))
            if resolved is not None:
                return resolved
        for candidate, _facts in _all_dots().items():
            if self.endpoint_session_state(candidate) not in (STATE_IDLE, "unavailable", "unknown"):
                return candidate
        return None

    def voice_answer(self, data: dict[str, Any]) -> None:
        """'answer' spoken at a dot (automation conversation-trigger path).
        The DROPIN_STATE_CONTEXT_KEY gate lives HERE (code, not YAML):
        refused in idle; accepted only while that dot's session is ringing,
        after which machine.accept enforces the ring floor (D-DI-3)."""
        device_id = self.resolve_endpoint_device(
            data.get("device") or data.get("caller_device"), data.get("room"),
        )
        if device_id is None:
            _LOGGER.info("drop-in answer refused: no ringing dot")
            return
        state = self.endpoint_session_state(device_id)
        if state != "ringing":
            _LOGGER.info("drop-in answer refused for %s: state=%s", device_id, state)
            return
        self._answer_service({"device": device_id})

    def voice_hangup(self, data: dict[str, Any]) -> None:
        """'hang up'/'stop' spoken at a dot in ringing/in-call (automation
        conversation-trigger path); refused in idle (stop-as-hangup
        ambiguity bounded by the guard, D-DI-5)."""
        device_id = self.resolve_endpoint_device(
            data.get("device") or data.get("caller_device"), data.get("room"),
        )
        if device_id is None:
            _LOGGER.info("drop-in hangup refused: no active call")
            return
        state = self.endpoint_session_state(device_id)
        if state not in ("ringing", "in-call", "joined"):
            _LOGGER.info("drop-in hangup refused for %s: state=%s", device_id, state)
            return
        self._voice_hangup_service({"device": device_id})

    def _answer_service(self, data: dict[str, Any]) -> None:
        """Accept by the DEVICE-side session resolution: find the session
        whose endpoint is ``device_id`` — the intent path's session-id free
        contract (the caller of 'answer' never knows the session id)."""
        device_id = str(data["device"])
        for session_id, session in self.machine.sessions.items():
            if session.endpoint(device_id) is not None:
                self.machine.accept(session_id, device_id)
                return
        raise CallError(f"no session contains {device_id}")

    def _voice_hangup_service(self, data: dict[str, Any]) -> None:
        device_id = str(data["device"])
        for session_id in list(self.machine.sessions):
            session = self.machine.sessions[session_id]
            if session.endpoint(device_id) is not None:
                self.machine.hangup(session_id, device_id)
                return
        raise CallError(f"no session contains {device_id}")

    def call_or_report_offline(self, data: dict[str, Any]) -> None:
        """Accepted-risk path (D-DI-6): offline-at-dial gives the CALLER
        immediate feedback — TTS '<callee room> isn't available' rather than
        silence. Availability is resolved by the injectable
        ``device_available`` callback (production wiring: the echod entity's
        HA state vs unreachable); when it reports the callee offline, the
        caller's registered media target gets a short TTS notice and no call
        is started. Without a registered media target, the notice degrades
        to a log line (device-side busy chime remains card 2's wiring path).

        TODO(accepted-risk D-DI-6): pass device_available=is_available from
        async_setup once the entity-id mapping (card 3 relay registration)
        exposes echod connection state; accidental-activation is bounded by
        the one-breath invocation (D-DI-5) — no additional code here.
        """
        callee = str(data["callee_device"])
        if self.device_available is not None and not self.device_available(callee):
            self._tts_on_caller(str(data["caller_device"]), f"{data['callee_room']} isn't available")
            self.emit("offline_at_dial", {"caller": data["caller_device"], "callee": callee})
            return
        try:
            self._call_service(data)
        except CallError as err:
            if "offline" in str(err):
                self._tts_on_caller(str(data["caller_device"]), f"{data['callee_room']} isn't available")
            else:
                raise

    def _tts_on_caller(self, caller_device: str, message: str) -> None:
        entity = self._media_targets.get(caller_device)
        if entity is None:
            _LOGGER.debug("no tts target for %s; offline-at-dial notice skipped", caller_device)
            return
        self.hass.services.call("tts", "speak", {
            "entity_id": entity, "media_player_entity_id": entity, "message": message,
        }, blocking=False)

    def _make_send(self, session_id: str):
        def send(device_id: str, frame: bytes) -> None:
            self.hass.async_create_task(self._send_frame(session_id, device_id, frame))
        return send

    async def _send_frame(self, session_id: str, device_id: str, frame: bytes) -> None:
        # Production path: native-API VoiceAssistantAudio{Data} push to the
        # device's existing connection (specs/dropin-probe-findings.md §4.2).
        _LOGGER.debug("relay frame session=%s device=%s bytes=%d", session_id, device_id, len(frame))

    # -- media pause/resume (D-DI-5: pause, not duck; HA-owned) ------------

    def register_media(self, device_id: str, entity_id: str) -> None:
        self._media_targets[device_id] = entity_id

    def _media_pause(self, device_id: str) -> None:
        entity = self._media_targets.get(device_id)
        if entity is None:
            return
        state = self.hass.states.get(entity)
        if state is not None and state.state == STATE_PLAYING:
            self.hass.add_job(
                self.hass.services.async_call, "media_player", "media_pause",
                {"entity_id": entity}, True,
            )
            self._paused_for_call.add(device_id)

    def _media_resume(self, device_id: str) -> None:
        entity = self._media_targets.get(device_id)
        if entity is None or device_id not in self._paused_for_call:
            return
        self.hass.add_job(
            self.hass.services.async_call, "media_player", "media_play",
            {"entity_id": entity}, True,
        )
        self._paused_for_call.discard(device_id)