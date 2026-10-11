package intercom

import (
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
