"""EchoLocal Drop-In Intercom — constants.

D-DI decisions: specs/dropin-decisions.md (jagsta/echolocal).
"""
from __future__ import annotations

DOMAIN = "echolocal_dropin"

# D-DI-3: 3-second minimum audible ring on the callee before "answer" may
# connect. This floor lives HERE (HA session state machine), not only
# device-side.
RING_FLOOR_SECONDS = 3.0

# D-DI-5: max-duration kill-switch against forgotten open lines.
DEFAULT_MAX_CALL_SECONDS = 30 * 60

# Card A fix (live failure 14 Oct): an unanswered / undelivered RING ends here
# so the caller endpoint (STATE_DIALING) cannot wedge the machine busy forever.
# Long enough for D-DI-3's 3s ring floor + human answer, short enough that a
# failed transport retry is worth within a minute.
DEFAULT_RING_TIMEOUT_SECONDS = 30.0

# D-DI-4: single reconnect attempt on HA comeback after a drop. No retry storm.
MAX_RECONNECT_ATTEMPTS = 1

# D-DI-3 permission policy values for the per-pair map (source -> target).
PERMISSION_ALLOW_HOUSEHOLD = "allow_household"  # default for unset pairs
PERMISSION_ALLOW = "allow"
PERMISSION_DENY = "deny"

# Device states of an endpoint within a session (call-state owner, D-DI-4).
STATE_IDLE = "idle"
STATE_RINGING = "ringing"  # callee being rung
STATE_DIALING = "dialing"  # caller waiting for callee accept
STATE_IN_CALL = "in_call"
STATE_BUSY_REJECTED = "busy_rejected"

# Session states (star topology: ONE session, N endpoints, D-DI roadmap note).
SESSION_IDLE = "idle"
SESSION_RINGING = "ringing"
SESSION_ACTIVE = "active"
SESSION_RECONNECTING = "reconnecting"
SESSION_ENDED = "ended"

# Dot-published intercom session sensor values (the dot's own State strings,
# wiring.go session.go). "in-call" is hyphenated dot-side; the HA endpoint
# vocabulary (STATE_IN_CALL) uses the underscore form — the sensor value is
# the dot's word, never the machine's.
DOT_SESSION_ACCEPTED = ("in-call", "muted", "joined")

SERVICE_CALL = "call"
SERVICE_ACCEPT = "accept"
SERVICE_HANGUP = "hangup"
SERVICE_SET_PERMISSION = "set_permission"
SERVICE_VOICE_ANSWER = "voice_answer"
SERVICE_VOICE_HANGUP = "voice_hangup"

# D-DI-8 card 6: phone/web caller identity convention. An unregistered web
# caller posts its offer with source id "phone:<display-name>"; it is treated
# as an ordinary endpoint in the per-pair permission map (default
# allow-household, D-DI-3) with no discovery dependence.
PHONE_SOURCE_PREFIX = "phone:"


def is_phone_source(source: str) -> bool:
    """True when the caller id is a phone/web peer ("phone:<name>", D-DI-8c)."""
    return isinstance(source, str) and source.startswith(PHONE_SOURCE_PREFIX)


def phone_display_name(source: str) -> str:
    """Callee-facing ring-announcement name for a phone peer ("phone:jag" -> "jag")."""
    if not is_phone_source(source):
        return source
    return source[len(PHONE_SOURCE_PREFIX):]