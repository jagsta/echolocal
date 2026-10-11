"""EchoLocal Drop-In Intercom — HA integration entry point.

D-DI-4: this component is the call-state owner and frame relay. Device
chimes/LED flow through echod's existing expose; audio rides the existing
ESPHome native API connections (one perpetual turn per device — see
specs/dropin-probe-findings.md §4).
"""
from __future__ import annotations

import logging

from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant

from .const import DOMAIN
from .runtime import DropInRuntime

_LOGGER = logging.getLogger(__name__)

PLATFORMS: list[str] = ["select"]  # call-policy config entity


async def async_setup(hass: HomeAssistant, config: dict) -> bool:
    hass.data.setdefault(DOMAIN, {})
    # Register services eagerly from YAML setup: with config_flow false HA
    # creates no config entry unless one is set up, and the acceptance probe
    # of card 5.5 is that the services are callable after restart.
    from .runtime import DropInRuntime

    runtime = DropInRuntime(hass, None)
    await runtime.async_setup()
    hass.data[DOMAIN]["yaml"] = runtime
    return True


async def async_setup_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    runtime = DropInRuntime(hass, entry)
    await runtime.async_setup()
    hass.data.setdefault(DOMAIN, {})[entry.entry_id] = runtime
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    return True


async def async_unload_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    runtime: DropInRuntime | None = hass.data.get(DOMAIN, {}).pop(entry.entry_id, None)
    if runtime is not None:
        await runtime.async_unload()
    return await hass.config_entries.async_unload_platforms(entry, PLATFORMS)