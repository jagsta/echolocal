"""Call state machine for the EchoLocal drop-in intercom.

Pure-Python, transport-agnostic, and event-driven so it is unit-testable
without Home Assistant. The relay (frame fan-out) is layered on top via
pluggable transport callbacks.

Topology constraint (specs 44dacef, 3-way roadmap): a session is a STAR
around ONE call with an endpoint LIST. Endpoints live in a list and
`add_participant` is an operation on the session — no hardcoded pairwise
bridging. v1 exercises exactly two endpoints.

Decisions implemented here:
- D-DI-3: per-pair permission map, default allow-household; 3s ring floor.
- D-DI-4: session state ownership, single reconnect attempt on drop.
- D-DI-5: 30-minute max-duration kill-switch.
- D-DI-6: busy-reject; mutual dial resolved deterministically by lower
  device-id (deterministic tie-break so both HA instances converge).
"""
from __future__ import annotations

from dataclasses import dataclass, field
from threading import Lock
from typing import Callable

from .const import (
    DEFAULT_MAX_CALL_SECONDS,
    DEFAULT_RING_TIMEOUT_SECONDS,
    MAX_RECONNECT_ATTEMPTS,
    PERMISSION_ALLOW,
    PERMISSION_ALLOW_HOUSEHOLD,
    PERMISSION_DENY,
    RING_FLOOR_SECONDS,
    SESSION_ACTIVE,
    SESSION_ENDED,
    SESSION_IDLE,
    SESSION_RECONNECTING,
    SESSION_RINGING,
    STATE_BUSY_REJECTED,
    STATE_DIALING,
    STATE_IDLE,
    STATE_IN_CALL,
    STATE_RINGING,
    is_phone_source,
    phone_display_name,
)


class CallError(Exception):
    """Raised for invalid transitions or policy rejections."""


# ---------------------------------------------------------------------------
# Permissions: per-pair map (source -> target), default allow-household D-DI-3
# ---------------------------------------------------------------------------

PERMISSION_SCHEMA_FIELDS = ("mode",)


def normalize_pair(source: str, target: str) -> tuple[str, str]:
    if not source or not target:
        raise CallError("pair endpoints must be non-empty")
    return (source.strip().lower(), target.strip().lower())


class PermissionMap:
    """Per-pair permission map source->target (D-DI-3).

    Unset pairs default to allow-household so rollout behaves trust-wide
    without configuration.
    """

    def __init__(self, entries: dict[tuple[str, str], dict] | None = None) -> None:
        self._entries: dict[tuple[str, str], dict] = dict(entries or {})

    def set_pair(self, source: str, target: str, mode: str, **reserved) -> None:
        if mode not in (PERMISSION_ALLOW_HOUSEHOLD, PERMISSION_ALLOW, PERMISSION_DENY):
            raise CallError(f"unknown permission mode: {mode}")
        entry = {"mode": mode}
        for key in reserved:
            if key not in PERMISSION_SCHEMA_FIELDS:
                raise CallError(f"unknown schema field: {key}")
            entry[key] = reserved[key]
        self._entries[normalize_pair(source, target)] = entry

    def get_pair(self, source: str, target: str) -> dict:
        return dict(self._entries.get(normalize_pair(source, target), {}))

    def allowed(self, source: str, target: str) -> bool:
        """allow-household is the default for unset pairs (D-DI-3)."""
        mode = self.get_pair(source, target).get("mode", PERMISSION_ALLOW_HOUSEHOLD)
        return mode in (PERMISSION_ALLOW_HOUSEHOLD, PERMISSION_ALLOW)

    def all_entries(self) -> dict[tuple[str, str], dict]:
        return {k: dict(v) for k, v in self._entries.items()}


# ---------------------------------------------------------------------------
# Session model: star around ONE session with an endpoint LIST
# ---------------------------------------------------------------------------

@dataclass
class Endpoint:
    device_id: str
    room: str
    state: str = STATE_IDLE
    muted: bool = False

    def __post_init__(self) -> None:
        if not self.device_id or not self.room:
            raise CallError("endpoint needs device_id and room")


@dataclass
class Session:
    """One call session; endpoints are a LIST (3-way = additive)."""

    session_id: str
    state: str = SESSION_IDLE
    endpoints: list[Endpoint] = field(default_factory=list)
    started_at: float | None = None
    ring_started_at: float | None = None
    reconnects_used: int = 0

    def endpoint(self, device_id: str) -> Endpoint | None:
        for ep in self.endpoints:
            if ep.device_id == device_id:
                return ep
        return None

    def device_ids(self) -> list[str]:
        return [ep.device_id for ep in self.endpoints]


class CallStateMachine:
    """HA-side call-state owner + policy enforcement (D-DI-4).

    The machine never touches a transport directly; it emits typed events on
    `emit` so a thin integration layer can drive echod/native-API media and
    chimes. `now` is an injectable clock for tests.
    """

    def __init__(
        self,
        permissions: PermissionMap | None = None,
        emit: Callable[[str, dict], None] | None = None,
        now: Callable[[], float] | None = None,
        max_call_seconds: float = DEFAULT_MAX_CALL_SECONDS,
        ring_timeout_seconds: float = DEFAULT_RING_TIMEOUT_SECONDS,
    ) -> None:
        self.permissions = permissions or PermissionMap()
        self.emit = emit or (lambda kind, data: None)
        self.now = now or __import__("time").time
        self.max_call_seconds = max_call_seconds
        self.ring_timeout_seconds = ring_timeout_seconds
        self.sessions: dict[str, Session] = {}
        self._lock = Lock()  # call() is check-then-act; serialize it (HA is asyncio-serialized anyway)
        self._busy_rejects: dict[str, str] = {}  # device_id -> rejection reason
        self._reconnect_target: tuple[str, str] | None = None  # (callee, session)

    # -- device-level queries ---------------------------------------------

    def endpoint_state(self, device_id: str) -> str:
        for session in self.sessions.values():
            ep = session.endpoint(device_id)
            if ep is not None:
                return ep.state
        return self._busy_rejects.get(device_id, STATE_IDLE)

    def mid_call(self, device_id: str) -> bool:
        return self.endpoint_state(device_id) in (STATE_RINGING, STATE_DIALING, STATE_IN_CALL)

    # -- session lifecycle --------------------------------------------------

    def _find_pair_session(self, device_a: str, device_b: str) -> Session | None:
        """The live session between exactly this device pair, if any."""
        for session in self.sessions.values():
            ids = session.device_ids()
            if len(ids) == 2 and {device_a, device_b} == set(ids):
                return session
        return None

    def call(self, session_id: str, caller: Endpoint, callee: Endpoint) -> Session:
        """Service call: echolocal_dropin.call(target_room) (D-DI-4 owner).

        Serialized under an internal lock: the busy-check / mutual-dial
        tie-break / session-registration sequence is check-then-act and
        must not interleave (production HA is single-threaded asyncio, the
        lock also makes multi-threaded test harnesses sound).
        """
        with self._lock:
            return self._call_locked(session_id, caller, callee)

    def _call_locked(self, session_id: str, caller: Endpoint, callee: Endpoint) -> Session:
        # D-DI-3 permission lookup on the source->target pair. Phone peers
        # (card 6, D-DI-8c) key by their "phone:<name>" identity instead of a
        # room — they have no room; the id is the source.
        source = caller.device_id if is_phone_source(caller.device_id) else caller.room
        if not self.permissions.allowed(source, callee.room):
            self.emit("permission_denied", {"caller": caller.device_id, "callee": callee.device_id})
            raise CallError(f"call {caller.room} -> {callee.room} denied by permission map")

        # D-DI-6 mutual dial: when a live session already exists between the
        # same device pair, both endpoints are dialing each other. Resolve
        # deterministically by device-id BEFORE any state is accepted: the
        # lower-id endpoint is the caller, the higher-id caller is
        # busy-rejected with a chime. Arrival order must not matter — both HA
        # instances converge on the same winner (D-DI-6).
        mutual = self._find_pair_session(caller.device_id, callee.device_id)
        if mutual is not None:
            if caller.device_id > callee.device_id:
                # Arriving caller is the higher id: he loses outright; the
                # existing session (owned by the lower-id caller) stands.
                self._busy_reject(caller, callee)
                raise CallError(
                    f"mutual dial: {caller.device_id} loses to lower device-id {callee.device_id}"
                )
            owner = mutual.endpoints[0].device_id
            if owner != caller.device_id:
                # The existing session was owned by the higher id: re-own it so
                # the lower-id device becomes the caller. The session is
                # re-keyed to the winning call's session_id and the higher-id
                # initiator is busy-rejected so his side hears busy regardless
                # of which dial arrived first.
                loser = mutual.endpoint(owner)
                if loser is None:  # defensive: session always holds its owner
                    raise CallError(f"mutual dial: owner {owner} missing from session")
                self._busy_reject(loser, callee)
                self._busy_rejects.pop(caller.device_id, None)
                caller.state = STATE_DIALING
                callee.state = STATE_RINGING
                mutual.endpoints = [caller, callee]
                mutual.ring_started_at = self.now()
                self.sessions.pop(mutual.session_id, None)
                mutual.session_id = session_id
                self.sessions[session_id] = mutual
                self.emit(
                    "ring",
                    {"session": session_id, "callee": callee.device_id, "caller": caller.device_id},
                )
                self.emit(
                    "mutual_dial_reowned",
                    {"session": session_id, "caller": caller.device_id, "loser": loser.device_id},
                )
                return mutual
            # Lower id already owns the session: fall through to the normal
            # busy-reject path below (the arriving higher-id caller is
            # rejected there because its state is mid-call).

        # D-DI-6 busy-reject: either endpoint mid-call rejects with busy chime.
        # A fresh call attempt from a previously-rejected caller clears its flag.
        self._busy_rejects.pop(caller.device_id, None)
        for device in (caller, callee):
            endpoint = self.endpoint_state(device.device_id)
            if self.mid_call(device.device_id) or endpoint == STATE_BUSY_REJECTED:
                self._busy_reject(caller, callee)
                raise CallError(f"{device.device_id} is mid-call (busy)")

        # D-DI-6 mutual dial: deterministic winner is lower device-id.
        winner, loser = (
            (caller, callee) if caller.device_id <= callee.device_id else (callee, caller)
        )
        if self.mid_call(loser.device_id) or winner.device_id != caller.device_id and self.mid_call(caller.device_id):
            self._busy_reject(caller, callee)
            raise CallError("mutual dial loser is busy")

        session = Session(session_id=session_id, state=SESSION_RINGING)
        caller.state = STATE_DIALING
        callee.state = STATE_RINGING
        session.endpoints = [caller, callee]
        session.ring_started_at = self.now()
        self.sessions[session_id] = session
        self._busy_rejects.pop(caller.device_id, None)
        self._busy_rejects.pop(callee.device_id, None)
        # D-DI-8 card 6: phone peers carry their display name straight through
        # to the ring announcement (no discovery dependence).
        ring_data = {
            "session": session_id,
            "callee": callee.device_id,
            "caller": caller.device_id,
        }
        if is_phone_source(caller.device_id):
            ring_data["caller_name"] = phone_display_name(caller.device_id)
        self.emit("ring", ring_data)
        return session

    def _busy_reject(self, caller: Endpoint, callee: Endpoint) -> None:
        self._busy_rejects[caller.device_id] = STATE_BUSY_REJECTED
        self.emit("busy_chime", {"to_device": caller.device_id})

    def accept(self, session_id: str, device_id: str, at: float | None = None) -> None:
        """'answer' — permitted only after the enforced 3s ring floor (D-DI-3)."""
        session = self._require(session_id)
        ep = session.endpoint(device_id)
        if ep is None or ep.state != STATE_RINGING:
            raise CallError(f"{device_id} is not the ringing callee")
        at = self.now() if at is None else at
        elapsed = at - (session.ring_started_at if session.ring_started_at is not None else at)
        if elapsed < RING_FLOOR_SECONDS:
            # D-DI-3 anti-ambush: too early, accept refused (device stays ringing).
            self.emit("accept_too_early", {"session": session_id, "device": device_id, "elapsed": elapsed})
            raise CallError(f"ring floor not met: {elapsed:.2f}s < {RING_FLOOR_SECONDS}s")
        self._connect(session, at)

    def add_participant(self, session_id: str, endpoint: Endpoint, permissions_ok: bool = True) -> None:
        """3-way roadmap hook: star relay takes an endpoint LIST (specs 44dacef).

        v1 does not invoke it; the operation exists so 3-way becomes additive.
        """
        session = self._require(session_id)
        if session.state != SESSION_ACTIVE:
            raise CallError("can only add participants to an active session")
        if self.mid_call(endpoint.device_id):
            raise CallError(f"{endpoint.device_id} is mid-call (busy)")
        if not permissions_ok:
            raise CallError("participant denied by permission map")
        endpoint.state = STATE_IN_CALL
        session.endpoints.append(endpoint)
        self.emit("participant_added", {"session": session_id, "device": endpoint.device_id})

    def _connect(self, session: Session, at: float) -> None:
        session.state = SESSION_ACTIVE
        session.started_at = at
        for ep in session.endpoints:
            ep.state = STATE_IN_CALL
        self.emit("connect", {"session": session.session_id, "endpoints": session.device_ids(),
                              "caller": session.endpoints[0].device_id})

    def hangup(self, session_id: str, device_id: str | None = None, at: float | None = None) -> None:
        """'hang up'/'stop'/button — ends chime both ends, media resumes (D-DI-5)."""
        session = self._require(session_id)
        at = self.now() if at is None else at
        if device_id is not None and session.endpoint(device_id) is None:
            raise CallError(f"{device_id} is not in session {session_id}")
        self._teardown(session, reason="hangup", at=at)

    def gc_tick(self, at: float | None = None) -> None:
        """D-DI-5 max-duration kill-switch + ring timeout.

        Two sweeps against leaked sessions:
        - forgotten ACTIVE lines end at max_call_seconds (original D-DI-5),
        - an unanswered RINGING session ends at ring_timeout_seconds. Without
          this the caller endpoint stays STATE_DIALING forever (mid_call ->
          busy) and every later dial from that dot is rejected. The failure
          mode was live-observed 14 Oct 2026: a ring that never reached the
          callee's dot stuck the caller in dialing; both dots dialable again
          only after an HA restart. Default 30s; sessions carry their own
          ring_started_at so nothing else tracks the deadline.
        """
        at = self.now() if at is None else at
        for session in list(self.sessions.values()):
            if session.state == SESSION_ACTIVE and session.started_at is not None:
                if at - session.started_at >= self.max_call_seconds:
                    self._teardown(session, reason="max_duration", at=at)
            elif session.state == SESSION_RINGING:
                if (
                    self.ring_timeout_seconds > 0
                    and session.ring_started_at is not None
                    and at - session.ring_started_at >= self.ring_timeout_seconds
                ):
                    self._teardown(session, reason="ring_timeout", at=at)

    def drop(self, session_id: str, device_id: str) -> None:
        """A remote endpoint dropped (e.g. device offline).

        D-DI-4: on HA comeback after a drop, one reconnect attempt allowed;
        callee hears an end-chime (never silence). No retry storm.
        """
        session = self._require(session_id)
        ep = session.endpoint(device_id)
        if ep is None:
            raise CallError(f"{device_id} is not in session {session_id}")
        if session.reconnects_used >= MAX_RECONNECT_ATTEMPTS:
            self._teardown(session, reason="drop_no_retry", at=self.now())
            return
        session.reconnects_used += 1
        session.state = SESSION_RECONNECTING
        self._reconnect_target = (device_id, session_id)
        self.emit("end_chime", {"to_device": device_id})  # callee end-chime D-DI-4
        self.emit("reconnect_attempt", {"session": session_id, "device": device_id})

    def try_reconnect(self, session_id: str, device_id: str) -> bool:
        """Return True if the dropped endpoint was re-admitted (once only)."""
        session = self._require(session_id)
        target = self._reconnect_target
        if target != (device_id, session_id) or session.state != SESSION_RECONNECTING:
            raise CallError("no pending reconnect for this device")
        ep = session.endpoint(device_id)
        if ep is None:
            raise CallError(f"{device_id} is not in session {session_id}")
        ep.state = STATE_IN_CALL
        session.state = SESSION_ACTIVE
        self._reconnect_target = None
        self.emit("connect", {
            "session": session_id,
            "endpoints": session.device_ids(),
            # Same shape as _connect's event: `caller` is what the runtime's
            # connect fan-out keys on — without it the caller dot gets the
            # callee's peerless dropin_answer join and never offers, so a
            # mid-call reconnect cannot resume the caller's leg (D-DI-4).
            "caller": session.endpoints[0].device_id,
        })
        return True

    def _teardown(self, session: Session, reason: str, at: float) -> None:
        session.state = SESSION_ENDED
        for ep in session.endpoints:
            ep.state = STATE_IDLE  # media resume orchestration happens in HA layer
        self.sessions.pop(session.session_id, None)
        self._reconnect_target = None
        # busy flags are call-scoped: once the call they were refused against
        # is gone, they must not leak onto the next ring (e.g. a device that
        # was rejected as a caller must be able to receive a call after the
        # busy session ends).
        for d in session.device_ids():
            self._busy_rejects.pop(d, None)
        for d in session.device_ids():
            self.emit("end_chime", {"to_device": d, "reason": reason})
        self.emit("teardown", {"session": session.session_id, "reason": reason, "at": at,
                               "endpoints": session.device_ids()})

    def set_muted(self, session_id: str, device_id: str, muted: bool) -> None:
        """HA-side mute visibility (D-DI-6): echoes the device-side mute
        toggle (card 2 wiring.ToggleMute → TextSensor) into the session so
        HA state always shows it. Transmit enforcement is device-side.
        """
        session = self._require(session_id)
        ep = session.endpoint(device_id)
        if ep is None:
            raise CallError(f"{device_id} is not in session {session_id}")
        if session.state != SESSION_ACTIVE:
            raise CallError("mute is only meaningful in-call (D-DI-6)")
        ep.muted = muted
        self.emit("mute_changed", {"session": session_id, "device": device_id, "muted": muted})

    def _require(self, session_id: str) -> Session:
        session = self.sessions.get(session_id)
        if session is None:
            raise CallError(f"unknown session {session_id}")
        return session