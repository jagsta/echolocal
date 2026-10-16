"""Device map: HA device registry ids -> dot facts (node slug, area, ip).

Card A (t_d767ce0a): the automations carry real assist-satellite device ids
(verified live 14 Oct). This module resolves a device_id to:
  - the esphome node the dot publishes its entities under (node slug) — used
    to build the esphome.<node>_dropin_dial action call, and
  - availability, from the dot's assist-satellite entity state.

Device ids are stable HA identifiers, discovered once and pinned here; the
area_aliases table maps area_id -> device via this file (single source).
"""
from __future__ import annotations

import logging

from .const import STATE_IDLE

_LOGGER = logging.getLogger(__name__)

# device_id (assist-satellite HA device in the device registry) -> {
#   node:  esphome node slug HA names the dot's entities with,
#   area:  HA area_id the dot lives in,
#   host:  the dot's LAN hostname (D-P2P-5: the signalling peer),
#   satellite_entity: the dot's assist_satellite entity id,
#   session_sensor: the dot-published intercom session text sensor
# }
DROPIN_DEVICE_MAP: dict[str, dict[str, str]] = {
    # lounge dot (biscuit_puffin, 192.168.10.254) — assist satellite device id
    "17b8d542d93b7f00a01a79c7e9edcf60": {
        "node": "lounge",
        "area": "little_lounge",
        "host": "lounge-dot.jagbains.org",
        "satellite_entity": "assist_satellite.lounge_assist_satellite",
        "speaker_entity": "media_player.lounge_speaker",
        # t_79ebd16b: the ring announce speaks THIS name, not the raw area id.
        # The callee hears "the lounge is dropping in", not "little lounge" —
        # the area_id ('little_lounge', from the HA registry) is house wiring,
        # not a spoken label.
        "announce_name": "lounge",
        "session_sensor": "sensor.little_lounge_lounge_microphone_intercom_session",
    },
    # kitchen dot (biscuit, 192.168.10.99)
    "acf8f22077b452856f9ef46024f95d57": {
        "node": "kitchen",
        "area": "kitchen",
        "host": "kitchen-dot.jagbains.org",
        "satellite_entity": "assist_satellite.kitchen_kitchen_assist_satellite",
        "speaker_entity": "media_player.kitchen_kitchen_speaker",
        "announce_name": "kitchen",
        "session_sensor": "sensor.kitchen_kitchen_microphone_intercom_session",
    },
}

# The dots' LAN DNS zone (D-P2P-5): room-dot.<domain> resolves on the LAN and
# is what the caller dot POSTs its SDP offer to. Verified live 15 Oct 2026:
# kitchen-dot.jagbains.org -> 192.168.10.99, lounge-dot.jagbains.org ->
# 192.168.10.254.
DOT_DOMAIN = "jagbains.org"

# area_id -> dot device id (area_aliases.json's authoritative area_ids)
AREA_TO_DEVICE: dict[str, str] = {
    facts["area"]: device_id
    for device_id, facts in DROPIN_DEVICE_MAP.items()
}

# area_id -> dot LAN hostname (D-P2P-5), the same single source as above.
AREA_TO_HOST: dict[str, str] = {
    facts["area"]: f"{facts['node']}-dot.{DOT_DOMAIN}"
    for facts in DROPIN_DEVICE_MAP.values()
}


def dot_for(device_id: str) -> dict[str, str] | None:
    """Facts for an HA device id, or None when it is not a known dot."""
    return DROPIN_DEVICE_MAP.get(str(device_id))


def callee_announce_name(caller_room: str) -> str:
    """The spoken caller label for the ring announce (t_79ebd16b).

    The CALLER's room, normalized for the callee's ears: when a dot in
    device_map claims this room, its announce_name is the human label
    ('lounge' for the little_lounge dot — the raw area id 'little lounge' is
    house wiring, not speech). Falls back to the underscore-stripped room for
    unmapped rooms (phone peers never reach this — they carry their display
    name before the call).
    """
    for known in DROPIN_DEVICE_MAP.values():
        if caller_room in (known["area"], known["node"]):
            return known.get("announce_name") or caller_room.replace("_", " ")
    return caller_room.replace("_", " ")


def node_for(device_id: str) -> str | None:
    facts = dot_for(device_id)
    return facts["node"] if facts else None


def dot_host(device_id: str) -> str | None:
    """The dot's LAN hostname, or None when the endpoint is not a known dot
    (a phone: peer, for instance — D-P2P-1 keeps that leg off the direct path).
    """
    facts = dot_for(device_id)
    if facts is None:
        return None
    return facts.get("host") or f"{facts['node']}-dot.{DOT_DOMAIN}"


def session_sensor_for(device_id: str) -> str | None:
    facts = dot_for(device_id)
    return facts["session_sensor"] if facts else None


def is_dot_offline(state: str | None) -> bool:
    """A dot is offline when HA reports it absent or unreachable."""
    return state in (None, "", "unavailable", "unknown")


def dot_available(hass, device_id: str, getter=None) -> bool:
    """Production device_available probe: the dot's assist-satellite state.

    Uses the injected getter (hass.states.get in production, a fake in tests)
    so this stays unit-testable without Home Assistant.
    """
    facts = dot_for(device_id)
    if facts is None:
        return False
    get = getter
    if get is None and hass is not None:
        # hass (production: hass.states.get) or a states-registry-like object
        # that already exposes .get (test fakes)
        get = hass.states.get if hasattr(hass, "states") else hass.get
    if get is None:
        return False
    state = get(facts["satellite_entity"])
    return state is not None and not is_dot_offline(state.state)


def session_state(hass, device_id: str, getter=None) -> str:
    """The dot-published intercom session state (idle/ringing/...) or idle."""
    sensor = session_sensor_for(device_id)
    if sensor is None:
        return STATE_IDLE
    get = getter
    if get is None and hass is not None:
        # hass (production: hass.states.get) or a states-registry-like object
        # that already exposes .get (test fakes)
        get = hass.states.get if hasattr(hass, "states") else hass.get
    if get is None:
        return STATE_IDLE
    state = get(sensor)
    if state is None or is_dot_offline(state.state):
        return STATE_IDLE
    return state.state