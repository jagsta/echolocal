// Package intercom implements the device-side session layer of the drop-in
// intercom (specs/dropin-decisions.md D-DI-1..7, card 2 of 6).
//
// v1 shape (D-DI-4, resolved by specs/dropin-probe-findings.md): the call rides
// existing ESPHome native API messages. Each device holds ONE perpetual
// VoiceSatellite turn for the call's duration — mic frames flow device→HA via
// SendAudio from a dedicated mic.Source listener ("intercom"), and the peer's
// audio arrives from HA as VoiceAssistantAudio frames, played on a dedicated
// config.StreamIntercom lane of the speaker mixer. Signalling (ring / accept /
// hang up / busy / mute) is ordinary entity/Action traffic; the HA-side
// component (card 3) owns relay and permissions.
//
// Wake-word suppression (D-DI-5) is DELIBERATE POLICY, implemented at two
// intercept points, not left to go-esphome-device's ErrTurnInProgress accident:
//
//  1. INTERCEPT POINT A (wake path, before StartTurn): wake detection is
//     paused entirely while intercom state != idle — intercom registers a
//     pause with internal/feature/detect (the seam the webrtc "call" feature
//     already uses via PausesWake). The detect engine's OnDetect therefore
//     never hands us a slot, and no wake StartTurn is even attempted.
//  2. INTERCEPT POINT B (button path, before StartTurn/Turn.Start): the
//     physical Action button tap calls voice.Get().Action() → Satellite
//     Start → turn.Start; intercom registers a guard via voice.RegisterGuard
//     that is checked before any turn starts (wired in feature/voice). Same
//     guarantee: no StartTurn while a call owns the turn.
//
// If both intercepts somehow miss, go-esphome-device still refuses a second
// StartTurn (ErrTurnInProgress) — the library accident is the backstop, not
// the mechanism.
package intercom

import (
	"sync"
	"time"
)

// State is the device-side session state exposed to Home Assistant.
type State string

const (
	Idle    State = "idle"
	Ringing State = "ringing"
	InCall  State = "in-call"
	Muted   State = "muted"

	// Joined is reserved for the 3-way drop-in roadmap (specs/dropin-decisions.md
	// roadmap): a third device joins an existing call. Not produced in v1 — the
	// machine keeps it as a room in the enum so multi-party stays additive.
	Joined State = "joined"
)

// RingFloor is the minimum audible ring before accept may connect (D-DI-3,
// anti-ambush rule). Amended (Jag, 16 Oct, t_903017d5): 1 SECOND — the
// announce TTS now precedes the ring on the HA side (speak-before-ring), so
// the floor no longer needs its old 3s headroom. The device refuses an early
// connect.
const RingFloor = 1 * time.Second

// Session is the intercom state machine. Pure state and timing only — audio
// movement lives in wiring.go, entities in session.go, so every rule here is
// unit-testable without hardware.
type Session struct {
	mu   sync.Mutex
	now  State
	rang time.Time // when ringing began (ring floor reference)

	// dial is the session id of the dial THIS dot is currently answering
	// (t_469de14a freshness contract): armed by RingWith (the dot's
	// dropin_dial), confirmed by Accept, cleared by End. The offer gate
	// compares the OFFER's session id against it, so a stale or duplicated
	// offer belonging to the PRIOR call on a REUSED session id (ids derive
	// from caller+callee device ids, so consecutive dials repeat them) can
	// never auto-answer a fresh ring — the fresh re-dial's dot genuinely
	// rings and WAITS for the button/HA accept. Empty dial fails closed.
	dial string

	// endpoints is the call's participant set, modeled as a LIST per the
	// 3-way roadmap constraint (specs/dropin-decisions.md): v1 exercises
	// exactly two, but nothing here hard-codes that count — AddParticipant
	// grows the set in place once join offers exist (card 5 / relay card).
	endpoints []string

	// clock is swappable so ring-floor and timing tests don't sleep.
	clock func() time.Time

	// maxDuration is the kill-switch budget from connect (D-DI-5: default 30m,
	// configurable; <= 0 disables the kill-switch).
	maxDuration time.Duration

	// Changed fires on every accepted state transition. wiring uses it for
	// chimes/LED; session.go publishes it to the HA text sensor.
	Changed []func(from, to State)

	// Ended fires when the machine self-ends a call (kill-switch expiry).
	// wiring tears down the turn and plays the end chime.
	Ended []func(reason string)

	// killTimer is swappable for tests; returns a cancel func.
	killTimer func(d time.Duration, fire func()) (stop func())
	stopKill  func()

	// authorized records that HA's connect (or the dot-native accept that
	// precedes it) opened this call. It is what the callee's D-P2P-4/5 offer
	// gate consults, so a stray offer can never start media on an idle dot.
	authorized bool

	pendingEnd string
}

// New builds the session. maxDuration <= 0 disables the kill-switch.
func New(maxDuration time.Duration) *Session {
	s := &Session{now: Idle, maxDuration: maxDuration, killTimer: afterFunc, clock: time.Now}
	s.stopKill = func() {}
	return s
}

func (s *Session) emit(from, to State) {
	for _, f := range s.Changed {
		f(from, to)
	}
}

// Now reports the current state.
func (s *Session) Now() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now
}

// Busy reports whether the device is mid-call (D-DI-6 busy-reject).
func (s *Session) Busy() bool { return s.Now() != Idle && s.Now() != Ringing }

// Ring marks an incoming drop-in WITHOUT a session id (legacy/test shape).
// Production dials carry the id and go through RingWith; an id-less ring
// leaves dial as-is, so existing tests and callers keep their semantics —
// with the one t_469de14a guarantee that a REAL dial (RingWith) is what arms
// the offer gate's freshness match.
func (s *Session) Ring() bool { return s.RingWith("") }

// RingWith marks an incoming drop-in from HA's dropin_dial, recording the
// session id of the dial THIS dot is answering (t_469de14a). Returns false
// when busy (device mid-call rejects new incoming calls — D-DI-6): ringing
// over a call is refused, re-ringing while already ringing is a no-op
// success. An id-carrying ring always (re)arms dial — a RE-RING of the same
// dial confirms it; an id-less echo leaves any recorded id alone.
func (s *Session) RingWith(session string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.now {
	case Idle:
		s.now, s.rang = Ringing, s.clock()
		if session != "" || s.dial == "" {
			s.dial = session
		}
		s.emit(Idle, Ringing)
		return true
	case Ringing:
		return true
	default:
		return false
	}
}

// CanAccept reports whether an accept is connectable right now: ringing and
// the ring floor elapsed. The device refuses an early connect (D-DI-3); the
// caller-side HA component should honor the same floor, but the device never
// trusts the network for this rule.
func (s *Session) CanAccept() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now == Ringing && s.clock().Sub(s.rang) >= RingFloor
}

// OfferAuthorized reports whether this dot may auto-answer an inbound
// dot↔dot offer (D-P2P-4/5) for the session `session`.
//
// It is the callee-side counterpart of the phone pairing gate (feature/call
// phoneGate.check, D-DI-9b): fail-closed, and decided by the session machine
// rather than by anything the network asserts. Allowed states:
//
//   - Ringing for the session THIS dot was dialed on — HA's dropin_dial put
//     this dot here, which IS the authorization. The caller only learns the
//     peer host from the same connect event that fires the offer, so the
//     offer can legitimately arrive before this dot has processed its own
//     dropin_connect; the ring must therefore answer. FRESHNESS (16 Oct
//     live defect, t_469de14a): a re-dial seconds after a hangup REUSES the
//     session id (ids derive from caller+callee device ids) while the
//     prior call's leftover offer was still eligible, and the bare
//     "Ringing ⇒ authorized" rule admitted it for the new ring — the dot
//     auto-connected with no button press and no HA accept. The gate now
//     compares the offer's session id against dial (armed by THIS dial's
//     dropin_dial): the id is the per-dial discriminator because HA now
//     generates it per dial (runtime.py) — the dot-side match is the
//     enforcement backstop. An offer carrying NO id is admitted only when
//     no dial id is recorded (pre-t_469de14a peers / unit-test shapes).
//   - a live call opened by Accept (dot-native accept) or CallerJoin (HA's
//     connect) — "InCall-with-connect", the state the D-P2P-5 accept path
//     leaves behind.
//
// Everything else (idle, ended, and any state that never saw an authorized
// call) refuses.
func (s *Session) OfferAuthorized(session string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.now {
	case Ringing:
		if session == "" {
			// Id-less offer: admitted only when no dial id is standing —
			// fail closed once a real dial has armed the freshness match.
			return s.dial == ""
		}
		return s.dial == session
	case InCall, Muted, Joined:
		if session == "" {
			return s.authorized
		}
		return s.authorized && s.dial == session
	default:
		return false
	}
}

// Accept connects the call. Refused unless ringing and the ring floor has
// elapsed. Returns the state after the attempt.
func (s *Session) Accept() (State, bool) {
	s.mu.Lock()
	if s.now != Ringing || s.clock().Sub(s.rang) < RingFloor {
		now := s.now
		s.mu.Unlock()
		return now, false
	}
	from := s.now
	s.now = InCall
	s.rang = time.Time{}
	s.authorized = true // this call is authorized: the gate admits offers
	s.endpoints = append(s.endpoints[:0],
		"caller", "callee") // v1 shape: exactly two; see AddParticipant
	s.mu.Unlock()

	s.emit(from, InCall)
	if s.maxDuration > 0 {
		s.armKill(s.maxDuration)
	}
	return InCall, true
}

// CallerJoin connects the CALLER side of an HA-mediated dial (card A,
// t_d767ce0a): the HA runtime invokes dropin_connect on the caller dot once
// the callee accepted. Unlike Accept (anti-ambush guarded, D-DI-3) this is a
// trusted local action from the household's HA — allowed straight from Idle
// and from Ringing (the callee-dot re-ringing case). The session id is
// recorded into dial (t_469de14a) so an offer answered in-call (the caller
// can legitimately be re-offered to after an offer retry) matches the gate's
// freshness discriminator. Returns the state after the attempt.
func (s *Session) CallerJoin(session string) (State, bool) {
	s.mu.Lock()
	switch s.now {
	case Idle, Ringing:
	default:
		now := s.now
		s.mu.Unlock()
		return now, false
	}
	from := s.now
	s.now = InCall
	s.rang = time.Time{}
	s.authorized = true // HA's connect authorized this call (D-P2P-4/5 gate)
	if session != "" || s.dial == "" {
		s.dial = session
	}
	s.endpoints = append(s.endpoints[:0], "caller", "callee")
	s.mu.Unlock()

	s.emit(from, InCall)
	if s.maxDuration > 0 {
		s.armKill(s.maxDuration)
	}
	return InCall, true
}

// Mute toggles mic transmit off in-call (D-DI-6). Only meaningful in-call:
// muted is a sub-state of InCall, never an idle or ringing condition.
func (s *Session) Mute(on bool) (State, bool) {
	s.mu.Lock()
	if s.now != InCall && s.now != Muted && s.now != Joined {
		now := s.now
		s.mu.Unlock()
		return now, false
	}
	from := s.now
	to := Muted
	if !on {
		to = InCall
	}
	if from == to {
		s.mu.Unlock()
		return to, true
	}
	s.now = to
	s.mu.Unlock()

	s.emit(from, to)
	return to, true
}

// AddParticipant grows the call's endpoint set (3-way roadmap constraint: the
// endpoint list is a list — joining is additive, not a refactor; v1 never
// calls it and stays exactly two). No-op refused when not in a call.
func (s *Session) AddParticipant(id string) bool {
	s.mu.Lock()
	if s.now != InCall && s.now != Muted && s.now != Joined {
		s.mu.Unlock()
		return false
	}
	for _, e := range s.endpoints {
		if e == id {
			s.mu.Unlock()
			return true
		}
	}
	s.endpoints = append(s.endpoints, id)
	from, joined := s.now, Joined
	if from == InCall || from == Muted {
		s.now = joined
	}
	s.mu.Unlock()

	s.emit(from, joined)
	return true
}

// Endpoints returns a copy of the call's participant list.
func (s *Session) Endpoints() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.endpoints...)
}

// End tears the session down from any active state. reason follows the call
// through Ended subscribers ("hangup", "expired", "dropped").
func (s *Session) End(reason string) bool {
	s.mu.Lock()
	if s.now == Idle {
		s.mu.Unlock()
		return false
	}
	from := s.now
	s.now = Idle
	s.rang = time.Time{}
	s.authorized = false // no call: the offer gate is closed again
	s.endpoints = s.endpoints[:0]
	s.dial = "" // dial epoch consumed: a re-dial re-arms via RingWith
	stop := s.stopKill
	s.pendingEnd = reason
	s.mu.Unlock()

	stop()
	s.emit(from, Idle)

	s.mu.Lock()
	reason, s.pendingEnd = s.pendingEnd, ""
	s.mu.Unlock()
	if reason != "" && reason != "hangup" {
		for _, f := range s.Ended {
			f(reason)
		}
	}
	return true
}

// armKill schedules the kill-switch (device self-ends call+turn at expiry,
// D-DI-5). killTimer is swappable for tests. The stopKill swap happens under
// s.mu: End() reads stopKill under the same lock, and the kill timer fires on
// its own goroutine, so an unlocked write races with a concurrent End.
func (s *Session) armKill(d time.Duration) {
	s.mu.Lock()
	s.stopKill()
	s.stopKill = s.killTimer(d, func() { s.End("expired") })
	s.mu.Unlock()
}

func afterFunc(d time.Duration, fire func()) (stop func()) {
	t := time.AfterFunc(d, fire)
	return func() { t.Stop() }
}
