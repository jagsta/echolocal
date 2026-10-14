"""Card A (t_d767ce0a) tests: device_map resolution + runtime dot-transport.

device_map is pure-Python (getter-injected) so it tests without Home
Assistant (D-DI-7). The map pins the two live dots' assist-satellite device
ids (verified via conversation triggers on 14 Oct 2026).
"""
from __future__ import annotations

import pytest

from custom_components.echolocal_dropin.const import STATE_IDLE
from custom_components.echolocal_dropin.device_map import (
    AREA_TO_DEVICE,
    DROPIN_DEVICE_MAP,
    dot_available,
    dot_for,
    is_dot_offline,
    node_for,
    session_sensor_for,
    session_state,
)

LOUNGE = "17b8d542d93b7f00a01a79c7e9edcf60"
KITCHEN = "acf8f22077b452856f9ef46024f95d57"


class FakeState:
    def __init__(self, state: str) -> None:
        self.state = state


class FakeStates:
    def __init__(self, states: dict[str, str]) -> None:
        self._states = {k: FakeState(v) for k, v in states.items()}

    def get(self, entity_id: str):
        return self._states.get(entity_id)


def test_map_pins_both_live_dots():
    assert set(DROPIN_DEVICE_MAP) == {LOUNGE, KITCHEN}
    assert DROPIN_DEVICE_MAP[KITCHEN]["node"] == "kitchen"
    assert DROPIN_DEVICE_MAP[LOUNGE]["node"] == "lounge"


def test_area_resolution_and_lookup():
    assert AREA_TO_DEVICE["kitchen"] == KITCHEN
    assert AREA_TO_DEVICE["little_lounge"] == LOUNGE
    assert dot_for(KITCHEN)["session_sensor"] == "sensor.kitchen_kitchen_microphone_intercom_session"
    assert node_for(KITCHEN) == "kitchen"
    assert dot_for("dev-not-a-dot") is None
    assert node_for("dev-not-a-dot") is None
    assert session_sensor_for("dev-not-a-dot") is None


def test_dot_available_follows_satellite_state():
    hass = FakeStates({"assist_satellite.kitchen_kitchen_assist_satellite": "idle"})
    assert dot_available(hass, KITCHEN) is True
    assert dot_available(FakeStates({"assist_satellite.kitchen_kitchen_assist_satellite": "unavailable"}), KITCHEN) is False
    assert dot_available(FakeStates({}), KITCHEN) is False
    # getter-injection variant (no hass)
    assert dot_available(None, KITCHEN, getter=lambda e: FakeState("idle")) is True
    assert dot_available(None, "dev-not-a-dot", getter=lambda e: FakeState("idle")) is False


def test_session_state_defaults_to_idle():
    hass = FakeStates({"sensor.kitchen_kitchen_microphone_intercom_session": "ringing"})
    assert session_state(hass, KITCHEN) == "ringing"
    assert session_state(FakeStates({}), KITCHEN) == STATE_IDLE
    assert session_state(None, "dev-not-a-dot") == STATE_IDLE


def test_is_dot_offline():
    for bad in (None, "", "unavailable", "unknown"):
        assert is_dot_offline(bad) is True
    assert is_dot_offline("idle") is False