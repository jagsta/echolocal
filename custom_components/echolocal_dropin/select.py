"""Config entity: per-pair permission map (source -> target), D-DI-3.

Select entity per source room; options are the permission modes. Schema
carries silent_connect as a reserved field (unused v1) so it is additive
later, not a schema migration.
"""
from __future__ import annotations

from homeassistant.components.select import SelectEntity
from homeassistant.core import HomeAssistant
from homeassistant.helpers.device_registry import DeviceEntryType, DeviceInfo
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .const import (
    DOMAIN,
    PERMISSION_ALLOW,
    PERMISSION_ALLOW_HOUSEHOLD,
    PERMISSION_DENY,
)
from .state_machine import PERMISSION_SCHEMA_FIELDS


async def async_setup_entry(
    hass: HomeAssistant,
    entry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    runtime = hass.data[DOMAIN][entry.entry_id]
    rooms: list[str] = entry.data.get("rooms", [])
    async_add_entities(
        DropInPermissionSelect(runtime, room) for room in rooms
    )


class DropInPermissionSelect(SelectEntity):
    """Per-source-room permission policy; pairs default allow-household."""

    _attr_options = [PERMISSION_ALLOW_HOUSEHOLD, PERMISSION_ALLOW, PERMISSION_DENY]
    _attr_has_entity_name = True

    def __init__(self, runtime, room: str) -> None:
        self._runtime = runtime
        self._room = room
        self._attr_name = f"Drop-in calling policy ({room})"
        slug = room.lower().replace(" ", "_")
        self._attr_unique_id = f"{DOMAIN}_{slug}_permission"
        self._attr_device_info = DeviceInfo(
            identifiers={(DOMAIN, slug)},
            name=f"Dropping endpoint {room}",
            entry_type=DeviceEntryType.SERVICE,
        )
        self._pairs: dict[str, dict] = {}  # target room -> schema entry

    @property
    def current_option(self) -> str | None:
        modes = {
            e.get("mode", PERMISSION_ALLOW_HOUSEHOLD) for e in self._pairs.values()
        }
        if len(modes) == 1:
            return next(iter(modes))
        return PERMISSION_ALLOW_HOUSEHOLD

    def map_pair(self, target: str, mode: str, **reserved) -> None:
        """Schema carries silent_connect reserved (unused v1, D-DI-3)."""
        entry = {"mode": mode}
        for key, value in reserved.items():
            if key not in PERMISSION_SCHEMA_FIELDS:
                raise ValueError(f"unknown schema field: {key}")
            entry[key] = value
        self._pairs[target] = entry
        self._runtime.permissions.set_pair(self._room, target, mode, **reserved)

    def select_option(self, option: str) -> None:
        for target in list(self._pairs):
            self._pairs[target] = {"mode": option}
            self._runtime.permissions.set_pair(self._room, target, option)