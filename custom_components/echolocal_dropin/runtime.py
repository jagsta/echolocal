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

import asyncio
import logging
from datetime import timedelta
from typing import Any

from homeassistant.const import STATE_PLAYING
from homeassistant.core import HomeAssistant, ServiceCall
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers.event import (
    async_track_state_change_event,
    async_track_time_interval,
)

from .const import (
    ANNOUNCE_ON_RING,
    DOMAIN,
    DOT_ANNOUNCE_PATH,
    DOT_ANNOUNCE_PORT,
    DOT_SESSION_ACCEPTED,
    PERMISSION_ALLOW,
    PERMISSION_ALLOW_HOUSEHOLD,
    PERMISSION_DENY,
    SERVICE_ACCEPT,
    SERVICE_CALL,
    SERVICE_DIAL,
    SERVICE_ANNOUNCE,
    SERVICE_HANGUP,
    SERVICE_SET_PERMISSION,
    SERVICE_VOICE_ANSWER,
    SERVICE_VOICE_HANGUP,
    SESSION_ACTIVE,
    SESSION_RINGING,
    STATE_IDLE,
    STATE_IN_CALL,
    STATE_RINGING,
    TRANSPORT_HARELAYS,
    TRANSPORT_P2P,
)
from .device_map import (
    AREA_TO_DEVICE,
    DROPIN_DEVICE_MAP,
    dot_available,
    dot_for,
    dot_host,
    session_sensor_for,
    session_state,
)
from .intent_router import IntentError, load_area_aliases, resolve_room
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
        self._session_sensor_unsubs: list[Any] = []
        # Ring-announce cue gate (t_61af01ce): entry option, default ON. The
        # __init__ path is only for production wiring; tests inject it after
        # __new__ the same way they inject device_available.
        self.announce_on_ring = True
        options = getattr(entry, "options", None) if entry is not None else None
        if options is not None:
            self.announce_on_ring = bool(options.get(ANNOUNCE_ON_RING, True))

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

        # t_3c196f3e / D-DI-6 offline-at-dial: the dial service resolves the
        # callee up front (aliases -> area -> dot) and reports an unavailable
        # or dotless callee as TTS at the caller WITHOUT starting a session,
        # so a phantom callee can never wedge the caller in DIALING.
        handle_dial = wrapped(self._dial_service)
        self.hass.services.async_register(DOMAIN, SERVICE_DIAL, handle_dial)

        handle_voice_answer = wrapped(self.voice_answer)
        handle_voice_hangup = wrapped(self.voice_hangup)

        self.hass.services.async_register(DOMAIN, SERVICE_VOICE_ANSWER, handle_voice_answer)
        self.hass.services.async_register(DOMAIN, SERVICE_VOICE_HANGUP, handle_voice_hangup)

        # t_47f64032 / D-ANN-2: the announce orchestration service. The handler
        # must be a coroutine function: HA runs a registered SYNC callable in
        # the executor, and _announce_service calls hass.async_create_task —
        # off-loop, which HA hard-raises on (live 19 Oct: the whole announce
        # traceback died before the nudge loop even started). Registered as
        # async it stays on the event loop and the task spawn is legal.
        self.hass.services.async_register(DOMAIN, SERVICE_ANNOUNCE, self._announce_service)

        # Card A fix (live failure 14 Oct): the machine only reaped ACTIVE
        # sessions at max_duration via nothing at all — gc_tick existed but no
        # caller ever ran it, so an unanswered RING wedged the caller endpoint
        # in dialing and every later dial was busy-rejected until restart.
        # Run the reaper every 5s: ring timeout (30s) + max-duration.
        async def _gc_tick(_now) -> None:
            await self.hass.async_add_executor_job(self.machine.gc_tick)

        self._gc_unsub = async_track_time_interval(self.hass, _gc_tick, timedelta(seconds=5))

        # t_108fc818: the dot is the ONLY observer of a physical accept
        # (action button) and of the accept it makes for itself. Until this
        # subscription existed the runtime READ the session sensor on demand
        # (utterance resolution) but never LISTENED, so a dot-native accept
        # painted the dot InCall while HA's machine stayed RINGING: no
        # connect event, StarRelay never opened, no audio anywhere, and the
        # 30s ring_timeout force-ended both dots (live 15 Oct 11:47).
        sensors = [
            sensor
            for sensor in (session_sensor_for(d) for d in _all_dots())
            if sensor
        ]
        if sensors:
            self._session_sensor_unsubs.append(
                async_track_state_change_event(
                    self.hass, sensors, self._async_session_sensor_event,
                )
            )

    async def async_unload(self) -> None:
        if getattr(self, "_gc_unsub", None) is not None:
            self._gc_unsub()
            self._gc_unsub = None
        for unsub in getattr(self, "_session_sensor_unsubs", []) or []:
            unsub()
        self._session_sensor_unsubs = []
        for session_id in list(self.machine.sessions):
            self.machine.hangup(session_id)

    # -- service impls (run in executor; state machine is sync) -----------

    def _resolve_callee(self, data: dict[str, Any]) -> tuple[str, str]:
        """(callee_device, callee_room) for a dial request, or raise CallError.

        t_3c196f3e: the raw automation used to pass the aspirational slug
        `echod-<room>` straight into machine.call, so a room with NO dot
        (spoken "lounge" -> area big_lounge; older "little lounge" before the
        automation normalised spaces) registered a RINGING session against a
        phantom endpoint and wedged the caller in DIALING until the 30s ring
        timeout, busy-rejecting every retry in the window. Resolution order:

        1. an explicit callee_device that IS a known dot → use it;
        2. area_aliases.json (spoken alias OR area id, e.g. "lounge" ->
           big_lounge, "little lounge" -> little_lounge) -> AREA_TO_DEVICE;
        3. the plain AREA_TO_DEVICE area-id lookup;
        4. otherwise REFUSE (CallError) — never dial a phantom endpoint.
        """
        callee_device = str(data.get("callee_device") or "")
        callee_room = str(data.get("callee_room") or "")
        if dot_for(callee_device) is not None:
            return callee_device, callee_room
        try:
            area = resolve_room(callee_room, self._aliases())
        except IntentError:
            area = None
        if area is None and AREA_TO_DEVICE.get(callee_room):
            area = callee_room  # an exact area id, even without an alias entry
        resolved = AREA_TO_DEVICE.get(area or "")
        if area is None or resolved is None:
            raise CallError(
                f"no drop-in dot in {callee_room!r} — cannot dial (D-DI-6)"
            )
        return resolved, area

    def _aliases(self) -> dict[str, str]:
        if getattr(self, "_alias_table", None) is None:
            self._alias_table = load_area_aliases()
        return self._alias_table

    # -- t_469de14a: per-dial session ids -----------------------------------
    # Session-id minting lives on the machine (fresh_session_id) so the
    # runtime and the intent path share one contract; the runtime honors
    # explicit session_id args verbatim and only auto-mints when absent.

    def _fresh_session_id(self, caller_device: str, callee_device: str) -> str:
        return self.machine.fresh_session_id(caller_device, callee_device)

    def _dial_service(self, data: dict[str, Any]) -> None:
        """echolocal_dropin.dial: offline-at-dial aware dial (D-DI-6).

        Resolves the callee via _resolve_callee (unknown/dotless room ->
        CallError), then reports an OFFLINE dot as a TTS notice at the caller
        with no session started (call_or_report_offline). The wrapped()
        boundary translates CallError into a clean HomeAssistantError.
        """
        callee_device, callee_room = self._resolve_callee(data)
        caller_device = str(data["caller_device"])
        if dot_for(caller_device) is None:
            resolved = AREA_TO_DEVICE.get(str(data.get("caller_room") or ""))
            if resolved is not None:
                caller_device = resolved
        room_label = callee_room.replace("_", " ")
        self.call_or_report_offline({
            "caller_device": caller_device,
            "caller_room": str(data.get("caller_room") or ""),
            "callee_device": callee_device,
            "callee_room": callee_room,
            "session_id": str(data.get("session_id") or self._fresh_session_id(
                caller_device, callee_device)),
            # the TTS notice speaks a friendly label, not the area id
            "_callee_label": room_label,
        })

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
        session_id = str(data.get("session_id") or self._fresh_session_id(
            caller_device, callee_device))
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
            # Jag ruling R1a (t_903017d5): SPEAK-BEFORE-RING. The announce cue
            # is spoken FIRST and awaited (capped), and only then is the
            # callee's dropin_dial dispatched — the callee hears "<caller> is
            # dropping in" in a quiet window before the chime/LED start, not
            # layered over ring audio. The reorder lives in an async job: the
            # machine emits from sync threads, and both the availability wait
            # and the TTS completion wait are async. The AVAILABILITY GATE
            # (R2, folded) sits before the speak: bounded wait (up to 10s,
            # short backoff) for the callee's assist_satellite to become
            # available; if it never does, log a WARN and dial anyway —
            # announce must never block or break dialing. CHECK-4 invariant
            # is unchanged: the cue is a tts announce (no mic, no voice turn,
            # never routed through the assist pipeline conversation), so the
            # reorder cannot open a 'heard/replying' window.
            # getattr: test runtimes built via __new__ (no __init__) default
            # to the ON state, exactly like production.
            if getattr(self, "announce_on_ring", True):
                try:
                    self.hass.add_job(self._announce_then_dial, dict(data))
                except Exception as err:  # noqa: BLE001 — same contract as _dot_action
                    _LOGGER.warning("drop-in announce handoff failed: %s", err)
                    self._teardown_failed_ring("announce_handoff", data.get("session"), err)
            else:
                self._dot_action(
                    data["callee"], "dropin_dial",
                    {
                        "session": data["session"],
                        "caller": data["caller"],
                        # D-P2P-7/8: the callee must know its transport before it
                        # can accept — post-P2P no call audio may enter the dot's
                        # ESPHome voice turn, including in the accept→connect
                        # window, so the pump's routing is set at the ring.
                        "transport": self._transport(data),
                    },
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
                if d == data.get("caller"):
                    # CALLER: peer=<other dot host>, it offers (D-P2P-5).
                    self._dot_action(d, "dropin_connect", self._connect_args(data, d))
                else:
                    # CALLEE (t_01e7db90): `dropin_connect` requires `peer` in
                    # HA's action schema, but the callee must never offer — it
                    # gets the peerless `dropin_answer` join instead.
                    self._dot_action(d, "dropin_answer", self._accept_args(data, d))
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

    # -- D-P2P transport selection (specs/dot-p2p-decisions.md) ------------

    def _transport(self, data: dict[str, Any]) -> str:
        """Which media transport this session uses (D-P2P-1/7).

        p2p only for a genuine dot↔dot pair — both endpoints are known dots
        with LAN hostnames. Anything else (a phone: peer, an unresolved slug)
        keeps the mothballed HA relay: the v1 send half stays alive and
        unreached at the p2p default, and the phone↔dot leg is untouched
        (D-P2P-1). Both event shapes are read: the ring event carries
        caller/callee, the connect event an endpoints list.
        """
        endpoints = [str(d) for d in (data.get("endpoints") or [])]
        if not endpoints:
            endpoints = [
                str(d) for d in (data.get("caller"), data.get("callee")) if d
            ]
        if len(endpoints) == 2 and all(dot_host(d) for d in endpoints):
            return TRANSPORT_P2P
        return TRANSPORT_HARELAYS

    def _other_endpoint(self, data: dict[str, Any], device_id: str) -> str | None:
        """The peer endpoint of ``device_id`` within this session."""
        candidates = [str(c) for c in (data.get("endpoints") or [])]
        if not candidates:
            # Same both-shape read as _transport: the ring event carries
            # caller/callee, the connect event an endpoints list.
            candidates = [
                str(c) for c in (data.get("caller"), data.get("callee")) if c
            ]
        for candidate in candidates:
            if candidate != str(device_id):
                return candidate
        return None

    def _connect_args(self, data: dict[str, Any], device_id: str) -> dict[str, str]:
        """dropin_connect args for one endpoint (D-P2P-2/5/7).

        The CALLER dot is the one that receives ``peer``: the callee's LAN
        hostname, built from the device_map facts. It is the caller that
        always offers (D-P2P-5); the callee gets the transport and no peer, so
        it only ever arms its /call/dotoffer answer gate.
        """
        caller = str(data.get("caller") or "")
        transport = self._transport(data)
        args = {"session": str(data.get("session") or ""), "transport": transport}
        if str(device_id) == caller and transport == TRANSPORT_P2P:
            host = dot_host(self._other_endpoint(data, device_id) or "")
            if host:
                args["peer"] = host
        return args

    # -- dot transport (Card A: the physical dial) -------------------------

    def _accept_args(self, data: dict[str, Any], device_id: str) -> dict[str, str]:
        """dropin_answer args for the CALLEE endpoint (D-P2P-5, t_01e7db90).

        HA's esphome integration marks every declared action arg REQUIRED
        (vol.Required per arg in its service schema), so the callee cannot be
        sent `dropin_connect` without `peer` — and per D-P2P-5 the callee must
        NOT get a peer (it only ever answers; a peer would make it offer). The
        callee therefore gets the dedicated peerless `dropin_answer` action
        (session + transport only), which joins the call dot-side (CallerJoin)
        and paints its session sensor in_call.
        """
        return {
            "session": str(data.get("session") or ""),
            "transport": self._transport(data),
        }

    # t_5c0b6869: call-lane undelivered handling — dropin_end retry budget.
    DOT_END_RETRIES = 2
    DOT_END_RETRY_DELAY_SECONDS = 2.0

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
            # device_id rides along for the undelivered path (t_5c0b6869):
            # the dropin_end retry must re-check that dot's session sensor.
            self.hass.add_job(self._dot_action_async, service, args,
                              session_id, device_id)
        except Exception as err:  # noqa: BLE001 — transport must never break the machine
            _LOGGER.warning("drop-in dot action %s failed: %s", service, err)
            self._teardown_failed_ring(action, session_id, err)

    async def _dot_action_async(self, service: str, args: dict[str, str],
                                session_id: str | None = None,
                                device_id: str | None = None) -> None:
        """One call-lane action, WITH the dot's answer (t_5c0b6869).

        All four call-lane actions declare Answers: true dot-side (each Run
        returns {"state": ...}), so the call requests return_response=True:
        a missing/empty response means the action vanished into a stale
        aioesphomeapi link (dot rebooted mid-call — the 19 Oct lounge warm
        boot held a dead connection while HA awaited with blocking=True, so
        the real failure only surfaced 30s later as a response timeout). The
        response handling keeps the previous teardown contract: a failed DIAL
        still tears down the ringing session via _teardown_failed_ring; an
        undelivered dropin_end gets a short bounded retry while the dot's
        sensor still paints in-call; everything else only warns.
        """
        try:
            # blocking=True surfaces the transport error (APIConnectionError
            # etc.) here instead of HA's service-execution error log.
            res = await self.hass.services.async_call(
                "esphome", service, args, blocking=True, return_response=True,
            )
        except Exception as err:  # noqa: BLE001
            _LOGGER.warning("drop-in dot action esphome.%s failed: %s", service, err)
            self._teardown_failed_ring(service, session_id, err)
            return
        if isinstance(res, dict) and res:
            _LOGGER.info("drop-in dot action esphome.%s answered: %s", service, res)
            return
        # Missing/empty answer -> UNDELIVERED (t_5c0b6869 finding 2).
        err = RuntimeError(
            f"no response from esphome.{service} — action may be undelivered "
            "(stale native-API link after a dot reboot?)"
        )
        _LOGGER.warning("drop-in dot action esphome.%s UNDELIVERED: %s", service, err)
        if str(service).endswith("_dial"):
            self._teardown_failed_ring(service, session_id, err)
        elif str(service).endswith("_end") and device_id is not None:
            await self._retry_dropin_end(device_id, service, args, session_id)

    async def _retry_dropin_end(self, device_id: str, service: str,
                                args: dict[str, str],
                                session_id: str | None = None) -> None:
        """Bounded retry for an undelivered dropin_end (t_5c0b6869).

        The dot may still be mid-reboot when the teardown lands; 1-2 quick
        retries (2s apart) usually catch the connection coming back. Each
        retry only fires while the dot's session sensor still shows an
        in-call state — idle/unknown means the end either landed or the call
        is already gone, and retrying would just churn the log.
        """
        sensor = session_sensor_for(device_id)
        if sensor is None:
            return
        for attempt in range(1, self.DOT_END_RETRIES + 1):
            await asyncio.sleep(self.DOT_END_RETRY_DELAY_SECONDS)
            state = getattr(self.hass.states.get(sensor), "state", None)
            if state not in DOT_SESSION_ACCEPTED:
                _LOGGER.info(
                    "drop-in dot action esphome.%s: %s sensor %s is %s — no retry needed",
                    service, device_id, sensor, state,
                )
                return
            _LOGGER.warning(
                "drop-in dot action esphome.%s retry %d/%d (%s still in-call: %s)",
                service, attempt, self.DOT_END_RETRIES, device_id, state,
            )
            try:
                res = await self.hass.services.async_call(
                    "esphome", service, args, blocking=True, return_response=True,
                )
            except Exception as err:  # noqa: BLE001
                _LOGGER.warning("drop-in dot action esphome.%s retry failed: %s",
                                service, err)
                res = None
            if isinstance(res, dict) and res:
                _LOGGER.info("drop-in dot action esphome.%s delivered on retry %d: %s",
                             service, attempt, res)
                return
        _LOGGER.error(
            "drop-in dot action esphome.%s UNDELIVERED after %d retries "
            "(device=%s session=%s) — dot-side session may linger until its "
            "own end/30-min reaper", service, self.DOT_END_RETRIES, device_id,
            session_id,
        )

    def _teardown_failed_ring(self, service: str, session_id: str | None,
                              err: Exception) -> None:
        """If a ring's transport to the callee dot failed, the dot will never
        ring and the session would sit RINGING (caller wedged STATE_DIALING)
        until the 30s gc ring timeout. End it now — the machine's teardown
        path chimes/resumes/ends both dots exactly like a real hangup and
        frees the caller to dial again immediately."""
        if session_id is None or not (str(service).endswith("_dial")
                                      or "announce" in str(service)):
            # Dial transport failures AND a failed announce handoff (the
            # add_job sync error in the speak-before-ring path — the ring
            # would never reach the dot through it) end a session early.
            return  # only ring transport failures end a session early
        session = self.machine.sessions.get(session_id)
        if session is None or session.state != SESSION_RINGING:
            return  # answered or already gone — nothing to reclaim
        _LOGGER.warning(
            "drop-in ring transport failed (session=%s): %s — tearing down",
            session_id, err,
        )
        self.machine.hangup(session_id)

    # -- dot-native accept: session sensor -> HA machine (t_108fc818) ------

    async def _async_session_sensor_event(self, event: Any) -> None:
        """HA state_changed callback for the dots' intercom session sensors."""
        data = event.data
        self.handle_session_sensor_change(
            data.get("entity_id"), data.get("old_state"), data.get("new_state"),
        )

    def _device_for_session_sensor(self, entity_id: str | None) -> str | None:
        for device_id in _all_dots():
            if session_sensor_for(device_id) == entity_id:
                return device_id
        return None

    def _ringing_session_for_device(self, device_id: str) -> Any | None:
        """The ONE ringing session whose CALLEE is this device, or None.

        Keyed by the endpoint in RINGING (the callee), never positionally: a
        session in any other state (ACTIVE/reconnecting/ended) or a device
        that is the caller (STATE_DIALING) must not match — that mismatch is
        what makes the accept path idempotent.
        """
        for session in self.machine.sessions.values():
            if session.state != SESSION_RINGING:
                continue
            endpoint = session.endpoint(device_id)
            if endpoint is not None and endpoint.state == STATE_RINGING:
                return session
        return None

    def handle_session_sensor_change(self, entity_id: str | None, old_state: Any,
                                     new_state: Any, at: float | None = None) -> str | None:
        """A dot-published session sensor transition -> the HA machine.

        A physical ACCEPT (action button) or any dot-native accept is
        executed entirely dot-side (wiring.Accept → Session.InCall): the dot
        chimes, opens its turn, and repaints this sensor — HA is never told
        anything. Without this bridge the machine stayed RINGING, no
        'connect' event fired, StarRelay never opened, neither dot got
        dropin_connect, and the 30s ring_timeout force-ended both dots with
        no audio anywhere (live 15 Oct 11:47). Here that transition becomes
        machine.accept() on the ringing session keyed by the CALLEE, which is
        the single accepted route to the relay: accept → connect event →
        _on_event → StarRelay.open + dropin_connect to both endpoints.

        Guarding:
        - only transitions INTO an accepted dot state (in-call/muted/joined)
          act; "ringing" is our OWN dial echo (dropin_dial painted the dot)
          and accepting it would answer our own ring.
        - a transition INTO "idle" out of an accepted state is a dot-native
          END (volume-down hangup, D-DI-5) and is mirrored into the HA machine
          (see _dot_ended_session) — without that mirror the peer's leg closes
          but its session/sensor/LED stay in-call until the 30-min reaper.
        - no ringing session for that callee (already ACTIVE, unknown, or
          this device is the caller) → ignore, so duplicate/late in-call
          paints, mute toggles, and the ring→in-call→muted sequence can never
          emit a second connect.
        - idempotent by CONSTRUCTION, not by a bookkeeping set: accept is only
          legal while the callee endpoint is RINGING and leaves the session
          ACTIVE, so every later accepted-state paint for the same session
          (mute toggles, an unmute back to in-call, a repaint) finds no
          ringing session and is dropped before it can emit a second
          'connect'. Nothing extra needs debouncing: the ring floor is
          enforced dot-side (D-DI-3) and re-enforced by machine.accept(); an
          accept refused as too early (clock skew) does not wedge anything —
          the dot's next accepted-state paint simply retries.
        """
        device_id = self._device_for_session_sensor(entity_id)
        if device_id is None:
            return None
        new_value = getattr(new_state, "state", None)
        if new_value not in DOT_SESSION_ACCEPTED:
            if new_value == STATE_IDLE and \
                    getattr(old_state, "state", None) in DOT_SESSION_ACCEPTED:
                return self._dot_ended_session(device_id, entity_id, at=at)
            return None
        session = self._ringing_session_for_device(device_id)
        if session is None:
            _LOGGER.debug(
                "drop-in dot accept ignored: no ringing session for %s (sensor=%s)",
                device_id, entity_id,
            )
            return None
        session_id = session.session_id
        try:
            self.machine.accept(session_id, device_id, at=at)
        except CallError as err:
            _LOGGER.info(
                "drop-in dot accept refused (session=%s device=%s): %s",
                session_id, device_id, err,
            )
            return None
        _LOGGER.info(
            "drop-in dot accept: session=%s callee=%s sensor %s -> %s -> connect",
            session_id, device_id, getattr(old_state, "state", None), new_state.state,
        )
        return session_id

    def _active_session_for_device(self, device_id: str) -> Any | None:
        """The ONE ACTIVE session whose endpoint for this device is in-call."""
        for session in self.machine.sessions.values():
            if session.state != SESSION_ACTIVE:
                continue
            endpoint = session.endpoint(device_id)
            if endpoint is not None and endpoint.state == STATE_IN_CALL:
                return session
        return None

    def _dot_ended_session(self, device_id: str, entity_id: str | None,
                           at: float | None = None) -> str | None:
        """A dot painting idle mid-call IS a dot-initiated hangup (D-DI-5).

        The dot's own hangup control is volume-down → wiring.Session.End
        (wiring.go:210-222): the dot chimes down, closes its leg, paints its
        session sensor idle — and HA is never told anything. The peer's leg
        then dies on ICE teardown while its session, sensor, LED and turn stay
        in-call with no audio until the max_call_seconds reaper (30 min); live
        15 Oct 15:33/15:34, both roles (kitchen volume-down stranded the
        lounge 34 s; lounge volume-down stranded the kitchen 13 s). D-DI-5
        says a callee ends on 'hang up'/'stop'/button, so that stranding
        contradicts a decided behaviour.

        Mirror it through the SAME machine.hangup() path the HA service uses:
        teardown → end chime + dropin_end for BOTH endpoints, peer sensor back
        to idle, peer leg closed. Ordering is naturally idempotent — teardown
        pops the session, so the HA-side teardown's own dropin_end (which
        makes both dots repaint idle) finds no ACTIVE session left and the
        dot-idle paints it produces are dropped here.

        Guarding: only an accepted-state → idle transition acts (a ringing →
        idle paint is the dot declining our own dial echo / its ring timeout,
        which the machine's own ring_timeout already ends), and only while the
        device's endpoint is genuinely in-call on an ACTIVE session. A dot
        that republishes idle while idle (boot, reconnect, unavailable →
        idle) therefore can never end anything.
        """
        session = self._active_session_for_device(device_id)
        if session is None:
            _LOGGER.debug(
                "drop-in dot end ignored: no active session for %s (sensor=%s)",
                device_id, entity_id,
            )
            return None
        session_id = session.session_id
        try:
            self.machine.hangup(session_id, device_id, at=at)
        except CallError as err:
            _LOGGER.info(
                "drop-in dot end ignored (session=%s device=%s): %s",
                session_id, device_id, err,
            )
            return None
        _LOGGER.info(
            "drop-in dot end: session=%s device=%s sensor %s -> idle -> teardown",
            session_id, device_id, entity_id,
        )
        return session_id

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

        Registered as ``echolocal_dropin.dial`` (t_3c196f3e); the dial
        service resolves the callee up front so a dotless/unknown room is a
        CallError before any session exists.
        """
        data = {k: v for k, v in data.items() if not k.startswith("_")}
        callee = str(data["callee_device"])
        label = str(data.get("_callee_label") or data["callee_room"]).replace("_", " ")
        if self.device_available is not None and not self.device_available(callee):
            self._tts_on_caller(str(data["caller_device"]), f"The {label} isn't available")
            self.emit("offline_at_dial", {"caller": data["caller_device"], "callee": callee})
            return
        try:
            self._call_service(data)
        except CallError as err:
            if "offline" in str(err):
                self._tts_on_caller(str(data["caller_device"]), f"The {label} isn't available")
            else:
                raise

    def _tts_on_caller(self, caller_device: str, message: str) -> None:
        entity = self._media_targets.get(caller_device)
        if entity is None:
            _LOGGER.debug("no tts target for %s; offline-at-dial notice skipped", caller_device)
            return
        # t_650b2f99: NEVER call hass.services.call() — the SYNC service API
        # blocks the event loop (the 16 Oct 16:56 freeze: the ring-announce
        # play_media stalled the loop, 'announce phase done' never logged,
        # dial never dispatched, HA UI + both dots' STT dead). This notice is
        # fire-and-forget, so hand the async call to the loop via add_job.
        self.hass.add_job(self.hass.services.async_call(
            "tts", "speak", {
                "entity_id": entity, "media_player_entity_id": entity, "message": message,
            }, blocking=False,
        ))

    # -- Jag ruling R1a/R2 (t_903017d5): speak-before-ring -----------------

    ANNOUNCE_AVAILABILITY_WAIT_SECONDS = 10.0
    ANNOUNCE_AVAILABILITY_POLL_SECONDS = 0.25
    ANNOUNCE_TTS_CAP_SECONDS = 5.0

    async def _announce_then_dial(self, ring_data: dict[str, Any]) -> None:
        """Announce cue FIRST, then dispatch the ring (R1a), behind an
        availability gate (R2).

        Order of operations on the ring event (all async — the machine's sync
        emit thread handed off via hass.add_job):
        1. AVAILABILITY GATE (R2): bounded wait (<=10s, 0.25s poll) for the
           callee's assist_satellite to leave unavailable/unknown — the 16 Oct
           live defect class was the TTS service warning 'entities missing or
           not currently available' because the satellite was transiently
           unavailable at dial time. If it never becomes available: WARN and
           PROCEED WITHOUT announce — the ring still fires; announce must
           never block or break dialing.
        2. ANNOUNCE (R1a): speak '<name> is dropping in' and AWAIT completion,
           capped at ~5s, so the callee hears the cue in a quiet window.
        3. RING: dispatch the callee's dropin_dial. The full failure
           propagation from the old inline path is preserved verbatim — a
           dead native-API transport tears the ringing session down instead
           of wedging caller/callee until the 30s gc ring timeout.

        All speak routes stay inside _announce_on_callee (unchanged
        mechanics); this wrapper only adds the waits and the ordering. An
        announce failure or cancellation NEVER prevents the dial dispatch.
        """
        session_id = str(ring_data.get("session") or "")
        try:
            ready = await self._await_announce_readiness(ring_data)
            if ready:
                try:
                    await asyncio.wait_for(
                        self._announce_on_callee_async(ring_data),
                        timeout=self.ANNOUNCE_TTS_CAP_SECONDS,
                    )
                except Exception as err:  # noqa: BLE001 — a failed cue never blocks the ring
                    if isinstance(err, asyncio.CancelledError):
                        raise
                    _LOGGER.warning("drop-in ring announce failed: %s", err)
            _LOGGER.info(
                "drop-in announce phase done (ready=%s); dispatching dial (session=%s callee=%s)",
                ready, session_id, ring_data.get("callee"),
            )
        finally:
            # The dial ALWAYS runs, announce or not, gate expired or not.
            self._dispatch_ring(ring_data)

    async def _await_announce_readiness(self, ring_data: dict[str, Any]) -> bool:
        """R2 availability gate: bounded wait for the callee dot's satellite
        entity to exist and not be unavailable/unknown. Returns True when the
        announce may proceed, False (WARN already logged) when the wait
        expired — dialing proceeds regardless."""
        callee = str(ring_data.get("callee") or "")
        entity = self._media_targets.get(callee)
        if entity is None:
            return False  # nothing to speak on; _announce_on_callee skips quietly
        getter = self.hass.states.get
        deadline = asyncio.get_running_loop().time() + \
            self.ANNOUNCE_AVAILABILITY_WAIT_SECONDS
        while True:
            state = getter(entity)
            if state is not None and state.state not in ("unavailable", "unknown"):
                return True
            if asyncio.get_running_loop().time() >= deadline:
                _LOGGER.warning(
                    "drop-in announce skipped: %s not available within %.0fs "
                    "(state=%s) — dialing anyway",
                    entity, self.ANNOUNCE_AVAILABILITY_WAIT_SECONDS,
                    getattr(state, "state", None),
                )
                return False
            await asyncio.sleep(self.ANNOUNCE_AVAILABILITY_POLL_SECONDS)

    def _dispatch_ring(self, data: dict[str, Any]) -> None:
        """Card A: transport the ring to the dot — invoke the callee's
        ESPHome dropin_dial action (chime/LED/session sensor paint happens
        dot-side the moment Session.Ring() fires). Split out of the inline
        ring path for the speak-before-ring reorder; failure propagation is
        unchanged: if the native-API transport is dead the dot will NEVER
        hear this ring — tear the session down immediately instead of leaving
        CALLEE=ringing / CALLER=dialing wedged until the 30s gc ring timeout
        (live 15 Oct break point, child-1 diagnosis: aioesphomeapi
        APIConnectionError "Not connected to kitchen")."""
        self._dot_action(
            data["callee"], "dropin_dial",
            {
                "session": data["session"],
                "caller": data["caller"],
                # D-P2P-7/8: the callee must know its transport before it
                # can accept — post-P2P no call audio may enter the dot's
                # ESPHome voice turn, including in the accept→connect
                # window, so the pump's routing is set at the ring.
                "transport": self._transport(data),
            },
            session_id=data["session"],
        )

    async def _announce_on_callee_async(self, ring_data: dict[str, Any]) -> None:
        """Speak the announce cue and await long enough for the announcement
        to actually play. t_650b2f99: the service calls are the ASYNC API run
        on the loop — the sync hass.services.call() here was the 16 Oct
        16:56 event-loop freeze (media_player.play_media with a TTS
        media-source blocked the loop; 'announce phase done' never logged,
        dial never dispatched, UI + both dots' STT dead).
        The legacy satellite route reports success the moment HA queues the
        TTS — real completion is not observable without a pipeline event, so
        the small settle delay + the caller-side cap bound it. The play_media
        route returns once the media session is enqueued."""
        await self._announce_on_callee(ring_data)
        await asyncio.sleep(0)  # yield to the loop so the service call lands

    async def _announce_on_callee(self, ring_data: dict[str, Any]) -> None:
        """Ring-announce cue (t_61af01ce, retargeted t_79ebd16b): '<name> is
        dropping in' spoken on the CALLEE's dot at ring, fire-and-forget.

        Mechanism v2 (t_79ebd16b): tts.speak targets the callee's dot-published
        media_player speaker entity (media_player.<node>_speaker), NOT the
        assist_satellite. Live 16 Oct 13:34: the satellite entity is transiently
        UNAVAILABLE while the ring owns it (WARNING 'entities missing or not
        currently available' at ring; Jag heard the announce only after
        teardown) — tts.speak to the satellite is queued/dropped during ring.
        The dot's media_player advertises MediaPlayerFeatureAnnounce
        (libcountertop mediaplayer player.go: Announcement=true, announce sound
        claim) which coexists with the intercom session claims — verified live
        idle AND accepted by tts.speak. Fallback: when the speaker entity is
        absent from the map (e.g. an unmapped dot) the old satellite route is
        used, so the cue degrades rather than disappears.

        No voice turn is started (tts.speak never sets StartConversation, no
        mic consumed — the D-P2P-8 bug class cannot recur), and no P2P offer
        exists yet (constructed at connect, not ring). The name is the caller's
        pairing/room identity the machine already carries
        (ring_data.caller_name; phone peers announce their display name; dot
        callers announce announce_name from device_map — 'lounge', not the raw
        area id 'little lounge', t_79ebd16b). Failures log only — a failed
        announce must never tear down a ring (unlike dropin_dial, see
        _teardown_failed_ring).
        """
        callee = str(ring_data.get("callee") or "")
        entity = self._media_targets.get(callee)
        name = str(ring_data.get("caller_name") or "someone").strip()
        if entity is None:
            _LOGGER.debug("no tts target for %s; ring announce skipped", callee)
            return
        # t_79ebd16b: prefer the dot's media_player speaker (its announce claim
        # coexists with the ring); fall back to the assist_satellite.
        speak_entity = entity
        facts = dot_for(callee)
        if facts is not None and facts.get("speaker_entity"):
            speak_entity = facts["speaker_entity"]
        message = f"{name} is dropping in"
        _LOGGER.info("drop-in ring announce: %s (callee=%s entity=%s)", message, callee, speak_entity)
        try:
            if speak_entity == entity:
                # Unmapped dot: the legacy satellite tts.speak route.
                # t_650b2f99: async API — the sync call() blocked the loop.
                await self.hass.services.async_call("tts", "speak", {
                    "entity_id": entity, "media_player_entity_id": entity, "message": message,
                }, blocking=False)
            else:
                # t_79ebd16b (live-verified 16 Oct 14:29): tts.speak REJECTS the
                # dot's media_player — HA's tts.speak service validator requires
                # MediaPlayerEntityFeature.ANNOUNCE in supported_features, and
                # the ESPHome integration maps this player's feature flags to
                # 1201677 without that bit (WARNING 'missing or not currently
                # available' every call). The route that DID play during ring:
                # media_player.play_media with media_content_type=announce and
                # a TTS media-source URL. Dot-side (libcountertop
                # mediaplayer player.go command()): Announcement=true routes to
                # p.announce() -> the announce sound claim -> PlayVoice, which
                # coexists with the intercom ring claim (verified: chime, WAV,
                # chime, WAV, chime, hangup all in one window).
                # Fire-and-forget. t_650b2f99: async API — the sync call()
                # with a TTS media-source was the 16 Oct event-loop freeze.
                await self.hass.services.async_call("media_player", "play_media", {
                    "entity_id": speak_entity,
                    "media_content_id": f"media-source://tts/cloud?message={message}",
                    "media_content_type": "announce",
                }, blocking=False)
        except Exception as err:  # noqa: BLE001 — never tear a ring down on a failed cue
            _LOGGER.warning("drop-in ring announce failed: %s", err)

    def _make_send(self, session_id: str):
        def send(device_id: str, frame: bytes) -> None:
            self.hass.async_create_task(self._send_frame(session_id, device_id, frame))
        return send

    async def _send_frame(self, session_id: str, device_id: str, frame: bytes) -> None:
        # Production path: native-API VoiceAssistantAudio{Data} push to the
        # device's existing connection (specs/dropin-probe-findings.md §4.2).
        _LOGGER.debug("relay frame session=%s device=%s bytes=%d", session_id, device_id, len(frame))

    # -- t_47f64032 / D-ANN-2/7/12/13: the announce orchestration service --

    # Capture-wait budget: the dot's window is a hard 15s cut plus turn wait
    # (10s) and chime lead-in — 30s covers every legitimate shape. Poll pace
    # 1s. After expiry the capture may still land (e.g. a slow origin): the
    # targets' own ~10s fetch retry (D-ANN-12, dot-side) then either catches
    # it or skips with a log — same degrade contract as an offline origin.
    ANN_CAPTURE_WAIT_SECONDS = 30.0
    ANN_CAPTURE_POLL_SECONDS = 1.0
    # D-ANN-12 HA half: a nudged receiver whose fetch still 404s skips on its
    # own; HA's nudge retry only covers transport hiccups, 3 quick tries.
    ANN_NUDGE_ATTEMPTS = 3
    ANN_NUDGE_DELAY_SECONDS = 1.0

    def _resolve_announce_targets(self, data: dict[str, Any]) -> list[str]:
        """The replay set (D-ANN-2): every registered echolocal dot except
        the invoking one, or exactly the dot in ``target_room``. Bad shapes
        are REFUSED loudly (CallError -> HomeAssistantError at the service
        boundary): the dot-side directory answers a wrong shape with HTTP 200
        [] silently, so HA is the loud refuser. Targets are the drop-in
        directory ONLY (device_map) — the spec puts every other media_player
        out of scope for v1 (D-ANN-1)."""
        caller_device = str(data.get("caller_device") or "")
        if dot_for(caller_device) is None:
            raise CallError(
                "announce needs the invoking dot's device id in caller_device "
                f"({caller_device!r} is not a registered echolocal dot)"
            )
        target_room = str(data.get("target_room") or "")
        if not target_room:
            return [d for d in _all_dots() if d != caller_device]
        # Same directory discipline as _resolve_callee: alias/area -> dot.
        try:
            area = resolve_room(target_room, self._aliases())
        except IntentError:
            area = None
        if area is None and AREA_TO_DEVICE.get(target_room):
            area = target_room
        resolved = AREA_TO_DEVICE.get(area or "")
        if area is None or resolved is None:
            raise CallError(f"no echolocal dot in {target_room!r} — cannot announce (D-ANN-2)")
        if resolved == caller_device:
            raise CallError(
                f"cannot announce to the invoking dot itself ({area!r} IS the caller, D-ANN-2)"
            )
        return [resolved]

    async def _announce_service(self, call: ServiceCall) -> None:
        """echolocal_dropin.announce — resolves the target set per D-ANN-2,
        refuses bad shapes loudly, then runs the async orchestration as a
        task: capture-kick on the origin, capture-completion poll (the dot's
        own ~10s fetch retry is too short to bridge a recording window), then
        one nudge per target. Never blocks the service call itself.

        Registered as a COROUTINE handler so it runs on the event loop — a
        sync handler lands in the executor and the async_create_task below
        is hard-refused off-loop (live 19 Oct failure)."""
        try:
            targets = self._resolve_announce_targets(dict(call.data))
        except CallError as err:
            raise HomeAssistantError(str(err)) from err
        self.hass.async_create_task(self._run_announce(dict(call.data), targets))

    async def _run_announce(self, data: dict[str, Any], targets: list[str]) -> None:
        caller_device = str(data["caller_device"])
        origin_host = dot_host(caller_device)
        _LOGGER.info(
            "echolocal announce: origin=%s targets=%s room=%s",
            origin_host, [dot_for(t)["node"] for t in targets],
            data.get("target_room") or "(all)",
        )
        # 1. Kick the origin's capture. The action answers instantly with the
        # lane state; a refusal (in-call, lane busy) shows in the reply — log
        # and stop, the dot already declined (D-ANN-13 dot-side).
        kick = await self._esphome_action(caller_device, "dropin_announce", {
            "room": str(data.get("target_room") or ""),
        })
        if not kick.get("announce_ok", True):
            _LOGGER.warning("announce capture refused by %s: %s", origin_host, kick)
            return
        if origin_host is None:
            _LOGGER.warning("announce: no LAN host for the origin dot; cannot observe capture")
            return
        # 2. Wait for capture completion (D-ANN-12 shape): poll the origin's
        # serve endpoint — a 200 carries X-Announce-Id; a 404 while the dot
        # logs "capturing" is the recording state, a 404 with no lane is the
        # capture having bailed. Either way HA stops waiting at the poll cap.
        cap_id = await self._await_capture(origin_host)
        if cap_id is None:
            _LOGGER.warning(
                "announce: no finished capture from %s within %.0fs; nudging targets anyway "
                "(their own fetch retry/skip takes it from here, D-ANN-12)",
                origin_host, self.ANN_CAPTURE_WAIT_SECONDS,
            )
        # 3. Nudge every target (D-ANN-2/12/13). async_call EVERYWHERE
        # (t_650b2f99): sync service calls reintroduce the event-loop freeze
        # that fail-fast tests pin. The play target is the RECEIVER dot's
        # media_player speaker (t_79ebd16b: the assist_satellite is
        # transiently unavailable during ring/in-call) — but the ESPhome
        # action is the vehicle; the receiver's dot-side guard does the rest.
        for device_id in targets:
            await self._nudge_target(device_id, origin_host)

    async def _esphome_action(self, device_id: str, action: str,
                              args: dict[str, str]) -> dict[str, Any]:
        """Invoke one dot's ESPHome action with the ASYNC service API
        (t_650b2f99). Returns the action's answer dict, or an error-shaped
        dict on transport failure — the caller logs the refusal and retries
        or stops; the dot-side guard is authoritative."""
        facts = dot_for(device_id)
        if facts is None:
            return {"announce_ok": False, "error": "not a registered dot"}
        service = f"{facts['node']}_{action}"
        try:
            # blocking + return_response (t_98e495f0): the dot's Run returns a
            # lane-state map; without return_response=True HA discards the
            # ResponseData and every nudge looks identical — a delivered play
            # and a refused/skipped one both log "accepted: None". The
            # esphome actions declare SUPPORTS_RESPONSE_OPTIONAL, so the
            # integration honours the request and hands the answer back.
            res = await self.hass.services.async_call(
                "esphome", service, args, blocking=True, return_response=True,
            )
            if isinstance(res, dict):
                if res.get("announce") is None:
                    res.setdefault("announce_ok", True)
                    _LOGGER.warning(
                        "esphome.%s to %s answered with no lane state (%s) — "
                        "delivery unverifiable", service, facts["node"], res)
                return res
            return {"announce_ok": True}
        except Exception as err:  # noqa: BLE001 — logged, never unwinds the loop
            _LOGGER.warning("esphome.%s to %s failed: %s", service, facts["node"], err)
            return {"announce_ok": False, "error": str(err)}

    async def _await_capture(self, origin_host: str | None) -> str | None:
        """Poll the origin's GET /announce/capture until it answers 200
        (capture done; the X-Announce-Id is returned for logging) or the
        budget expires (None). Transport errors and 404 (recording/bailed)
        are retried on the poll pace; a 410 (expired) ends the wait early —
        the capture will never come back."""
        if not origin_host:
            return None
        getter = self._announce_probe_getter()
        if getter is None:
            _LOGGER.warning("announce: no HTTP session available; capture poll skipped")
            return None
        url = f"https://{origin_host}:{DOT_ANNOUNCE_PORT}{DOT_ANNOUNCE_PATH}"
        deadline = asyncio.get_running_loop().time() + self.ANN_CAPTURE_WAIT_SECONDS
        while asyncio.get_running_loop().time() < deadline:
            status, cap_id = await getter(url)
            if status == 200:
                return cap_id
            if status == 410:  # gone: the window closed with no nudge
                _LOGGER.warning("announce: capture from %s already expired (410)", origin_host)
                return None
            # 404 while recording, or anything transient: keep polling.
            await asyncio.sleep(self.ANN_CAPTURE_POLL_SECONDS)
        return None

    def _announce_probe_getter(self):
        """The (url -> (status, x-announce-id)) probe used by _await_capture.
        Production: the shared aiohttp session with TLS verify OFF (the dots
        serve the D-CERT staged bundle, same posture as the dot-to-dot fetch,
        p2p_offer.go peerDotClient). Import is lazy + guarded: the full
        homeassistant.helpers.aiohttp_client import pulls zeroconf/ifaddr,
        which the unit-test venv does not carry — tests inject their own
        getter via _announce_probe instead."""
        probe = getattr(self, "_announce_probe", None)
        if probe is not None:
            return probe  # type: ignore[return-value]
        try:
            from homeassistant.helpers.aiohttp_client import (
                async_get_clientsession,
            )

            session = async_get_clientsession(self.hass, verify_ssl=False)

            async def probe(url: str) -> tuple[int, str | None]:
                try:
                    async with session.get(url) as resp:
                        if resp.status == 200:
                            return 200, resp.headers.get("X-Announce-Id")
                        return resp.status, None
                except Exception as err:  # noqa: BLE001 — origin may be mid-restart
                    _LOGGER.debug("announce capture poll: %s", err)
                    return 0, None

            self._announce_probe = probe
            return probe
        except Exception as err:  # noqa: BLE001 — test envs may lack the helper chain
            _LOGGER.debug("announce probe unavailable: %s", err)
            return None

    async def _nudge_target(self, device_id: str, origin_host: str) -> None:
        """Nudge ONE receiver (D-ANN-12/13). The receiver's own lane guard
        skips in-call / delays ringing dot-side; HA's job is the offline
        retry (quick, 3 tries) then an honest logged skip.
        t_eb8314c4: the answer names the OUTCOME via accepted/reason
        (accepted | capturing | playing | skipped-in-call | delayed); an
        older dot answers only the legacy announce lane key and we keep
        reading it."""
        facts = dot_for(device_id)
        node = facts["node"]
        for attempt in range(self.ANN_NUDGE_ATTEMPTS):
            res = await self._esphome_action(device_id, "dropin_play", {"origin": origin_host})
            if not res.get("announce_ok", True):
                _LOGGER.info("announce: nudge %s rejected (%s), attempt %d/%d",
                             node, res, attempt + 1, self.ANN_NUDGE_ATTEMPTS)
                await asyncio.sleep(self.ANN_NUDGE_DELAY_SECONDS)
                continue
            accepted = res.get("accepted")
            reason = res.get("reason")
            if accepted is None:
                # Pre-t_eb8314c4 dot (legacy contract): only the lane key.
                lane = res.get("announce")
                if lane is None:
                    _LOGGER.warning(
                        "esphome.%s to %s answered with no lane state (%s) — "
                        "delivery unverifiable", f"{node}_dropin_play", node, res)
                    return
                if lane != "idle":
                    _LOGGER.info("announce: nudge %s (attempt %d) answered lane busy: %s",
                                 node, attempt + 1, lane)
                    await asyncio.sleep(self.ANN_NUDGE_DELAY_SECONDS)
                    continue
                _LOGGER.info("announce: nudge %s (attempt %d) accepted (legacy lane answer: idle)",
                             node, attempt + 1)
                return
            if accepted:
                _LOGGER.info("announce: nudge %s (attempt %d) accepted: %s",
                             node, attempt + 1, reason)
                return
            if reason == "delayed":
                # t_eb8314c4: the dot deferred the play (receiver ringing) and
                # owns the outcome — it plays or logs the ring-never-resolved
                # skip. No re-nudge: a retry would race the deferred play.
                _LOGGER.info("announce: nudge %s (attempt %d) deferred dot-side "
                             "(receiver ringing; the dot logs the final outcome)", node, attempt + 1)
                return
            if reason in ("capturing", "playing"):
                _LOGGER.info("announce: nudge %s (attempt %d) answered lane busy: %s",
                             node, attempt + 1, reason)
                await asyncio.sleep(self.ANN_NUDGE_DELAY_SECONDS)
                continue
            # skipped-in-call (or any other named skip): the dot-side guard is
            # authoritative; an honest logged skip, no retry.
            _LOGGER.info("announce: nudge %s (attempt %d) skipped by dot: %s",
                         node, attempt + 1, reason)
            return
        _LOGGER.warning("announce: target %s skipped after %d nudges (offline/busy, D-ANN-12)",
                        node, self.ANN_NUDGE_ATTEMPTS)


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