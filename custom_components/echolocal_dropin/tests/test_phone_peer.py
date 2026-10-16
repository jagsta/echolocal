"""Card 6 (D-DI-8): phone/web caller acceptance parity.

A phone peer ("phone:<name>" source, unregistered, no discovery) is an
ordinary endpoint: default allow-household permission (D-DI-3), RINGING with
the enforced ring floor (1s since the t_903017d5 amendment), display name
carried through to the ring event.
"""
from __future__ import annotations

import pytest

from custom_components.echolocal_dropin.const import (
    PERMISSION_ALLOW_HOUSEHOLD,
    PHONE_SOURCE_PREFIX,
    SESSION_RINGING,
    STATE_DIALING,
    STATE_RINGING,
    is_phone_source,
    phone_display_name,
)
from custom_components.echolocal_dropin.state_machine import (
    CallError,
    CallStateMachine,
    Endpoint,
    PermissionMap,
)

from .test_state_machine import FakeClock, Recorder, make_machine


def test_phone_source_predicates():
    assert is_phone_source("phone:jag")
    assert not is_phone_source("dev-kitchen")
    assert phone_display_name("phone:jag") == "jag"
    # non-phone sources pass through unchanged
    assert phone_display_name("dev-kitchen") == "dev-kitchen"
    assert PHONE_SOURCE_PREFIX == "phone:"


def test_phone_offer_enters_ringing_with_floor():
    """Unknown phone-source caller rings the callee like any Dot caller."""
    machine, clock, rec = make_machine()
    session = machine.call(
        "di-phone",
        Endpoint("phone:jag", "phone"),
        Endpoint("dev-callee", "living"),
    )
    assert session.state == SESSION_RINGING
    assert machine.endpoint_state("dev-callee") == STATE_RINGING
    assert machine.endpoint_state("phone:jag") == STATE_DIALING
    # Ring floor enforced on the phone-peer ring exactly as D-DI-3 (1s
    # since the t_903017d5 amendment: a sub-floor accept is still refused).
    with pytest.raises(CallError, match="ring floor"):
        machine.accept("di-phone", "dev-callee", at=clock.t + 0.5)
    clock.advance(3.0)
    machine.accept("di-phone", "dev-callee")  # now past the floor
    ring = rec.kinds("ring")[-1]
    assert ring["caller"] == "phone:jag"


def test_phone_peer_resolves_under_set_permission():
    """A phone source is a first-class per-pair map key (D-DI-8c)."""
    permissions = PermissionMap()
    permissions.set_pair("phone:jag", "living", PERMISSION_ALLOW_HOUSEHOLD)
    entry = permissions.get_pair("phone:jag", "living")  # normalize_pair lowercases
    assert entry["mode"] == PERMISSION_ALLOW_HOUSEHOLD
    assert permissions.allowed("phone:jag", "living")
    # deny cuts it off at the machine layer.
    permissions.set_pair("phone:jag", "living", "deny")
    machine = CallStateMachine(permissions=permissions, emit=Recorder(), now=FakeClock())
    with pytest.raises(CallError, match="denied"):
        machine.call(
            "di-phone-denied",
            Endpoint("phone:jag", "phone"),
            Endpoint("dev-callee", "living"),
        )


def test_phone_display_name_in_ring_notification():
    """Ring event carries the phone display name for the announcement."""
    machine, _, rec = make_machine()
    machine.call(
        "di-phone-name",
        Endpoint("phone:jag", "phone"),
        Endpoint("dev-callee", "living"),
    )
    ring = rec.kinds("ring")[-1]
    assert ring["caller_name"] == "jag"
    # Dot callers do NOT gain the field (byte-identical parity, additive only).
    machine.hangup("di-phone-name")
    machine.call(
        "di-dot",
        Endpoint("dev-caller", "kitchen"),
        Endpoint("dev-callee", "living"),
    )
    dot_ring = rec.kinds("ring")[-1]
    # t_61af01ce: dot callers now carry caller_name TOO — their room label
    # (the announce cue speaks "<room> is dropping in"); only phone peers
    # announce the display NAME rather than a room.
    assert dot_ring["caller_name"] == "kitchen"