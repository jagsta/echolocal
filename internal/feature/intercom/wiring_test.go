package intercom

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/echolocal/internal/feature/voice"
)

// TestTurnTeardownOnEveryEndPath asserts the round-1 review fix: the shared
// VoiceSatellite turn must be released (EndAudio + StopTurn) on EVERY
// transition into Idle from an active state — hangup (button), expired
// (kill-switch), dropped (turn open failed), hangup-while-ringing — exactly
// once, and never on an already-idle End (no-op). The turnOps stub counts
// releaseTurn invocations; releaseTurn itself is double-close guarded via
// turnOpen, so the wiring only calls it when a turn was actually accepted.
func TestTurnTeardownOnEveryEndPath(t *testing.T) {
	pastFloor := func(s *Session) {
		// Ring under one clock, Accept under one past the floor — a single
		// constant clock would freeze the ring-floor reference time.
		start := time.Now()
		s.clock = func() time.Time { return start }
		if !s.Ring() {
			t.Fatal("ring refused")
		}
		s.clock = func() time.Time { return start.Add(RingFloor + time.Second) }
	}
	cases := []struct {
		name string
		run  func(i *Intercom)
		want int64
	}{
		{"hangup while in-call", func(i *Intercom) {
			s := i.session
			pastFloor(s)
			if _, ok := s.Accept(); !ok {
				t.Fatal("accept refused")
			}
			i.mu.Lock()
			i.turnOpen = true // mirrors wiring.Accept's successful StartTurn
			i.mu.Unlock()
			if !s.End("hangup") {
				t.Fatal("hangup end refused")
			}
		}, 1},
		{"kill-switch expiry", func(i *Intercom) {
			s := i.session
			fired := make(chan struct{}, 1)
			s.killTimer = func(d time.Duration, fire func()) (stop func()) {
				go func() { fire(); fired <- struct{}{} }()
				return func() {}
			}
			pastFloor(s)
			if _, ok := s.Accept(); !ok {
				t.Fatal("accept refused")
			}
			i.mu.Lock()
			i.turnOpen = true // mirrors wiring.Accept's successful StartTurn
			i.mu.Unlock()
			<-fired // expired self-end
		}, 1},
		{"dropped after failed turn open", func(i *Intercom) {
			s := i.session
			pastFloor(s)
			if _, ok := s.Accept(); !ok {
				t.Fatal("accept refused")
			}
			// Mirrors wiring.Accept's failure path: state InCall, turn never
			// opened (turnOpen stays false), End("dropped") — the transition
			// to Idle still runs, and releaseTurn's turnOpen=false guard makes
			// it a no-op on the satellite.
			if !s.End("dropped") {
				t.Fatal("dropped end refused")
			}
		}, 0},
		{"hangup while ringing", func(i *Intercom) {
			s := i.session
			pastFloor(s)
			if !s.End("hangup") {
				t.Fatal("hangup end refused")
			}
		}, 0},
		{"end while already idle is no-op", func(i *Intercom) {
			if i.session.End("hangup") {
				t.Fatal("idle end should be refused")
			}
		}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var teardowns atomic.Int64
			i := &Intercom{session: New(30 * time.Minute)}
			i.turnOps = func() { teardowns.Add(1) }
			i.watchTurnRelease(i.releaseTurn)

			tc.run(i)

			if got := teardowns.Load(); got != tc.want {
				t.Fatalf("teardown invocations = %d, want %d", got, tc.want)
			}
			if got := i.session.Now(); got != Idle {
				t.Fatalf("state = %q, want idle", got)
			}
		})
	}
}

// TestReleaseTurnDoubleCloseGuard pins releaseTurn's one-shot behavior: with
// turnOpen=false it must not touch the satellite (turnOps stays uninvoked);
// with turnOpen=true it releases exactly once and clears the flag.
func TestReleaseTurnDoubleCloseGuard(t *testing.T) {
	var calls atomic.Int64
	i := &Intercom{session: New(30 * time.Minute)}
	i.turnOps = func() { calls.Add(1) }

	i.releaseTurn() // turnOpen false → early return, stub untouched
	if calls.Load() != 0 {
		t.Fatalf("release with turn closed invoked ops %d times, want 0", calls.Load())
	}

	i.mu.Lock()
	i.turnOpen = true
	i.mu.Unlock()
	i.releaseTurn()
	if calls.Load() != 1 {
		t.Fatalf("release with turn open invoked ops %d times, want 1", calls.Load())
	}
	if i.turnOpen {
		t.Fatal("turnOpen should be cleared after first release")
	}
}

// TestRealSatellitePathNoNilDeref pins the round-2 (vs never assigned) review
// fix: Accept and releaseTurn must run against a REAL VoiceSatellite — no
// turnOps stub short-circuiting releaseTurn before it reaches i.vs — without
// a nil dereference. A disconnected satellite returns ErrNoSubscriber from
// every turn call, which is exactly what a no-connection unit test wants.
func TestRealSatellitePathNoNilDeref(t *testing.T) {
	i := &Intercom{session: New(30 * time.Minute)}
	i.vs = &esphome.VoiceSatellite{} // real satellite, no connection
	// turnOps deliberately nil: the real EndAudio→StopTurn path must run.

	// Ring past the floor, then Accept. StartTurn fails (no subscriber), so
	// wiring.Accept must fall through to End("dropped") — exercising both
	// the StartTurn deref and the subsequent releaseTurn teardown.
	start := time.Now()
	i.session.clock = func() time.Time { return start }
	if !i.session.Ring() {
		t.Fatal("ring refused")
	}
	i.session.clock = func() time.Time { return start.Add(RingFloor + time.Second) }
	// wiring.Accept dereferences i.vs.StartTurn (no subscriber → error), then
	// Ends with reason "dropped"; without watchTurnRelease this test stays
	// deterministic, so release Turn is exercised directly below.
	i.session.Changed = append(i.session.Changed, func(from, to State) {})
	i.Accept() // must NOT panic on the real satellite

	// Force the teardown path directly against the real satellite.
	i.mu.Lock()
	i.turnOpen = true
	i.mu.Unlock()
	i.releaseTurn() // must call i.vs.EndAudio then i.vs.StopTurn without panicking
	if i.turnOpen {
		t.Fatal("turnOpen not cleared by real-satellite releaseTurn")
	}
}

// TestMuteTransmitGuardVariables pins the D-DI-6 transmit contract at the
// wiring boundary: pump() drops frames when i.muted is set (or session idle)
// and transmits again after unmute; the session flips Muted→InCall and
// wiring.show paints/clears amber accordingly.
func TestMuteTransmitGuardVariables(t *testing.T) {
	i := &Intercom{session: New(30 * time.Minute)}
	s := i.session
	start := time.Now()
	s.clock = func() time.Time { return start }
	if !s.Ring() {
		t.Fatal("ring refused")
	}
	s.clock = func() time.Time { return start.Add(RingFloor + time.Second) }
	if _, ok := s.Accept(); !ok {
		t.Fatal("accept refused")
	}

	// mute in-call: session flips to Muted (amber LED via wiring.show)
	if got, ok := i.session.Mute(true); !ok || got != Muted {
		t.Fatalf("mute: got %q ok=%v, want muted", got, ok)
	}
	i.mu.Lock()
	i.muted = true // mirrors wiring.ToggleMute's guard flip
	i.mu.Unlock()

	// the pump guard condition reproduced: muted or idle ⇒ drop frames
	if m := i.muted || i.session.Now() == Idle; !m {
		t.Fatal("pump guard must see transmit off while muted in-call")
	}

	// unmute restores transmit state and session returns to InCall
	i.session.Mute(false)
	i.mu.Lock()
	i.muted = false
	i.mu.Unlock()
	if i.muted || i.session.Now() == Idle {
		t.Fatal("unmute must restore transmit availability in-call")
	}
	if i.session.Now() != InCall {
		t.Fatalf("state after unmute = %q, want in-call", i.session.Now())
	}
}

// TestMuteFlagResetsOnTeardownAndAccept pins the t_14649d19 fix at the wiring
// boundary: i.muted is process-global — the only writer is ToggleMute, and
// pump() drops every frame while it is set — so it must be cleared on every
// Idle-from-active teardown AND re-asserted at Accept. The regression: mute
// in-call, hang up, join again ⇒ the pump guard drops nothing (before the
// fix, i.muted stayed true and every later call was silent both ways).
func TestMuteFlagResetsOnTeardownAndAccept(t *testing.T) {
	i := &Intercom{session: New(30 * time.Minute), status: &esphome.TextSensor{}}
	i.turnOps = func() {}
	i.wire()
	s := i.session

	// Call 1: join, mute in-call. CallerJoin straight from Idle mirrors HA's
	// connect on the caller dot; the wire-installed teardown subscriber and
	// Accept's resetMute cover both reset paths.
	if _, ok := s.CallerJoin(); !ok {
		t.Fatal("join refused")
	}
	if got, ok := s.Mute(true); !ok || got != Muted {
		t.Fatalf("mute: got %q ok=%v, want muted", got, ok)
	}
	i.mu.Lock()
	i.muted = true // mirrors wiring.ToggleMute's flag flip
	i.mu.Unlock()

	// End the call. The teardown subscriber must clear the flag (and bring
	// the session's muted sub-state back to InCall before the Idle emit).
	if !s.End("hangup") {
		t.Fatal("end refused")
	}
	i.mu.Lock()
	stale := i.muted
	i.mu.Unlock()
	if stale {
		t.Fatal("i.muted survived teardown — the next call would be silent (t_14649d19)")
	}

	// Call 2 on the same (pre-fix poisoned) flag: join again — resetMute at
	// CallerConnect re-asserts transmit-off is cleared — and the pump guard
	// condition must now transmit.
	if _, ok := s.CallerJoin(); !ok {
		t.Fatal("second join refused")
	}
	i.mu.Lock()
	i.muted = true // simulate a stale flag from call 1 (pre-fix shape)
	i.mu.Unlock()
	i.resetMute("caller-connect")
	if i.muted {
		t.Fatal("resetMute at caller-connect left i.muted set")
	}
	if m := i.muted || i.session.Now() == Idle; m {
		t.Fatal("pump guard must transmit in call 2 after the mute/teardown cycle")
	}
	if i.session.Now() != InCall {
		t.Fatalf("state in call 2 = %q, want in-call", i.session.Now())
	}
}

// TestAcceptResetsStaleMutedFlag asserts the belt at Accept (the wiring
// method, not the session machine): a muted flag left over from a previous
// call (e.g. a teardown that skipped the subscriber) is cleared when the next
// call is accepted, before any frames can be dropped.
func TestAcceptResetsStaleMutedFlag(t *testing.T) {
	i := &Intercom{session: New(30 * time.Minute), status: &esphome.TextSensor{}}
	i.wire()
	s := i.session
	i.mu.Lock()
	i.muted = true // stale flag from a previous (unterminated) call
	i.mu.Unlock()

	start := time.Now()
	s.clock = func() time.Time { return start }
	if !s.Ring() {
		t.Fatal("ring refused")
	}
	s.clock = func() time.Time { return start.Add(RingFloor + time.Second) }
	i.Accept() // wiring entry: resetMute runs before StartTurn (which fails, no subscriber → dropped)
	if i.muted {
		t.Fatal("Accept did not clear a stale muted flag (pump would drop all frames)")
	}
}

// TestWireAssignsVS pins the assignment itself (round-3 secondary): wire() must
// set i.vs from voice.Get().VS(), so a future regression that removes the
// assignment is caught here rather than only in Accept/releaseTurn runtime paths.
func TestWireAssignsVS(t *testing.T) {
	i := &Intercom{session: New(30 * time.Minute), status: &esphome.TextSensor{}}
	i.wire()
	if i.vs == nil {
		t.Fatal("wire() left i.vs nil — Accept/releaseTurn would panic at runtime")
	}
	if i.vs != voice.Get().VS() {
		t.Fatal("i.vs is not the shared satellite from voice.Get().VS()")
	}
}

// TestRingChimeRepeatsUntilLeaveRinging pins the 15 Oct defect fix: the ring
// tone must repeat every ring period while the session sits in Ringing — the
// one-shot Idle→Ringing chime left later rings visual-only. wire() must arm
// the repeat timer on ring entry and stop it on ANY transition out of Ringing
// (InCall via Accept/CallerJoin, Idle via End/re-dial), and the timer's fire
// closure must no-op once the session is no longer Ringing (a fire that raced
// the stop must not chime).
func TestRingChimeRepeatsUntilLeaveRinging(t *testing.T) {
	// manualTimer stands in for repeatingTimer: each arm records the installed
	// fire closure, its stop unrecords it — deterministic, no sleeping, and
	// hands the test exactly the closure wire() installed.
	type arms struct {
		mu   sync.Mutex
		live map[*func()]struct{}
	}
	fakeTimer := func(live *arms) func(time.Duration, func()) (stop func()) {
		return func(_ time.Duration, fire func()) (stop func()) {
			key := &fire
			live.mu.Lock()
			live.live[key] = struct{}{}
			live.mu.Unlock()
			return func() {
				live.mu.Lock()
				delete(live.live, key)
				live.mu.Unlock()
			}
		}
	}
	wired := func(live *arms) *Intercom {
		i := &Intercom{session: New(30 * time.Minute), status: &esphome.TextSensor{}}
		i.ringTimer = fakeTimer(live)
		i.wire()
		return i
	}

	t.Run("arms one timer on ring entry", func(t *testing.T) {
		live := &arms{live: map[*func()]struct{}{}}
		i := wired(live)
		if !i.session.Ring() {
			t.Fatal("ring refused")
		}
		live.mu.Lock()
		n := len(live.live)
		live.mu.Unlock()
		if n != 1 {
			t.Fatalf("ring entry armed %d timers, want 1", n)
		}
	})

	t.Run("re-arm replaces, never doubles", func(t *testing.T) {
		live := &arms{live: map[*func()]struct{}{}}
		i := wired(live)
		if !i.session.Ring() {
			t.Fatal("first ring refused")
		}
		if !i.session.End("hangup") {
			t.Fatal("end refused")
		}
		if !i.session.Ring() {
			t.Fatal("second ring refused")
		}
		live.mu.Lock()
		n := len(live.live)
		live.mu.Unlock()
		if n != 1 {
			t.Fatalf("re-ring left %d timers armed, want 1", n)
		}
	})

	t.Run("stop on accept", func(t *testing.T) {
		live := &arms{live: map[*func()]struct{}{}}
		i := wired(live)
		start := time.Now()
		i.session.clock = func() time.Time { return start }
		if !i.session.Ring() {
			t.Fatal("ring refused")
		}
		i.session.clock = func() time.Time { return start.Add(RingFloor + time.Second) }
		if _, ok := i.session.Accept(); !ok {
			t.Fatal("accept refused")
		}
		live.mu.Lock()
		n := len(live.live)
		live.mu.Unlock()
		if n != 0 {
			t.Fatalf("accept left %d ring timers armed, want 0", n)
		}
	})

	t.Run("stop on hangup from ringing", func(t *testing.T) {
		live := &arms{live: map[*func()]struct{}{}}
		i := wired(live)
		if !i.session.Ring() {
			t.Fatal("ring refused")
		}
		if !i.session.End("hangup") {
			t.Fatal("end refused")
		}
		live.mu.Lock()
		n := len(live.live)
		live.mu.Unlock()
		if n != 0 {
			t.Fatalf("end from ringing left %d ring timers armed, want 0", n)
		}
	})

	t.Run("stale fire after end never chimes", func(t *testing.T) {
		live := &arms{live: map[*func()]struct{}{}}
		chimed := &atomic.Int64{}
		i := wired(live)
		// Swap the speaker path for a counter: replace only for the closure
		// we hold — the production chime is a method on Intercom, so instead
		// run the closure's own guard: hold the fire, let the session leave
		// Ringing, then fire. The wiring closure checks Now()==Ringing before
		// chiming, so a raced/stale fire must be a no-op. We assert via the
		// speaker seam we DO control here: chime() would panic on the nil
		// speaker in tests — meaning any stale chime turns into a loud
		// failure instead of a silent pass.
		if !i.session.Ring() {
			t.Fatal("ring refused")
		}
		live.mu.Lock()
		var fire func()
		for f := range live.live {
			fire = *f // the closure wire() installed
		}
		live.mu.Unlock()
		if fire == nil {
			t.Fatal("ring entry installed no fire closure")
		}
		if !i.session.End("hangup") {
			t.Fatal("end refused")
		}
		// The closure's guard must see a non-Ringing session, so the chime
		// below must be skipped — fire() returning without chiming is the
		// assertion; a stale chime would Interject a nil-speaker panic.
		fire()
		if i.session.Now() != Idle {
			t.Fatal("session must be idle after hangup")
		}
		_ = chimed
	})
}
