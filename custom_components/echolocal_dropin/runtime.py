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
from typing import Any

from homeassistant.const import STATE_PLAYING
from homeassistant.core import HomeAssistant, ServiceCall

from .const import (
    DOMAIN,
    PERMISSION_ALLOW,
    PERMISSION_ALLOW_HOUSEHOLD,
    PERMISSION_DENY,
    SERVICE_ACCEPT,
    SERVICE_CALL,
    SERVICE_HANGUP,
    SERVICE_SET_PERMISSION,
)
from .relay import StarRelay
from .state_machine import (
    Endpoint,
    PermissionMap,
    CallStateMachine,
)

_LOGGER = logging.getLogger(__name__)


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

    async def async_setup(self) -> None:
        async def handle_call(call: ServiceCall) -> None:
            await self.hass.async_add_executor_job(self._call_service, call.data)

        async def handle_accept(call: ServiceCall) -> None:
            await self.hass.async_add_executor_job(self._accept_service, call.data)

        async def handle_hangup(call: ServiceCall) -> None:
            await self.hass.async_add_executor_job(self._hangup_service, call.data)

        async def handle_set_permission(call: ServiceCall) -> None:
            await self.hass.async_add_executor_job(self._set_permission_service, call.data)

        self.hass.services.async_register(DOMAIN, SERVICE_CALL, handle_call)
        self.hass.services.async_register(DOMAIN, SERVICE_ACCEPT, handle_accept)
        self.hass.services.async_register(DOMAIN, SERVICE_HANGUP, handle_hangup)
        self.hass.services.async_register(DOMAIN, SERVICE_SET_PERMISSION, handle_set_permission)

    async def async_unload(self) -> None:
        for session_id in list(self.machine.sessions):
            self.machine.hangup(session_id)

    # -- service impls (run in executor; state machine is sync) -----------

    def _call_service(self, data: dict[str, Any]) -> None:
        caller_device = str(data["caller_device"])
        callee_device = str(data["callee_device"])
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
            silent_connect=data.get("silent_connect"),
        )

    # -- state machine events -> device/media actions ----------------------

    def _on_event(self, kind: str, data: dict[str, Any]) -> None:
        _LOGGER.debug("drop-in event %s %s", kind, data)
        if kind == "ring":
            # Callee chime + LED ring via echod's existing expose (D-DI-3).
            self._media_pause(data["callee"])
        elif kind == "connect":
            relay = self._relays.get(data["session"])
            if relay is None:
                relay = StarRelay()
                relay.bind(self._make_send(data["session"]))
                for d in data["endpoints"]:
                    relay.add(d)
                relay.open()
                self._relays[data["session"]] = relay
        elif kind == "teardown":
            relay = self._relays.pop(data["session"], None)
            if relay is not None:
                relay.close()
            for d in data.get("endpoints", []) or []:
                self._media_resume(d)
            # end chime + media resume on both ends emitted by machine already

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
            self.hass.services.call("media_player", "media_pause", {"entity_id": entity}, blocking=False)
            self._paused_for_call.add(device_id)

    def _media_resume(self, device_id: str) -> None:
        entity = self._media_targets.get(device_id)
        if entity is None or device_id not in self._paused_for_call:
            return
        self.hass.services.call("media_player", "media_play", {"entity_id": entity}, blocking=False)
        self._paused_for_call.discard(device_id)