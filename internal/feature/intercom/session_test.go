package intercom

import (
	"testing"
	"time"
)

func newTest(t *testing.T, now *time.Time) *Session {
	t.Helper()
	s := New(30 * time.Minute)
	s.clock = func() time.Time { return *now }
	return s
}

func TestStateTransitions(t *testing.T) {
	var now time.Time
	s := newTest(t, &now)
	var transitions []State
	s.Changed = append(s.Changed, func(_, to State) { transitions = append(transitions, to) })

	if s.Now() != Idle {
		t.Fatalf("fresh session: got %q, want idle", s.Now())
	}
	if s.Busy() {
		t.Fatal("fresh session must not be busy")
	}

	if !s.Ring() {
		t.Fatal("ring from idle must succeed")
	}
	if s.Now() != Ringing || s.Busy() {
		t.Fatalf("after ring: got %q busy=%v, want ringing busy=true", s.Now(), s.Busy())
	}
	// Re-ring while ringing is a no-op success.
	if !s.Ring() {
		t.Fatal("re-ring while ringing must succeed")
	}

	// Mute is meaningless outside a call.
	if _, ok := s.Mute(true); ok {
		t.Fatal("mute while ringing must be refused")
	}

	now = now.Add(RingFloor)
	if s.CanAccept() != true {
		t.Fatal("accept allowed after the ring floor")
	}
	if _, ok := s.Accept(); !ok {
		t.Fatal("accept after the floor must connect")
	}
	if s.Now() != InCall {
		t.Fatalf("after accept: got %q, want in-call", s.Now())
	}

	if ok := s.Ring(); ok {
		t.Fatal("ring during a call must be refused (busy-reject, D-DI-6)")
	}
	if s.Now() != InCall {
		t.Fatalf("busy-reject must not change state: got %q", s.Now())
	}

	if got, ok := s.Mute(true); !ok || got != Muted {
		t.Fatalf("mute in-call: got %q ok=%v, want muted", got, ok)
	}
	// Toggle back.
	if got, ok := s.Mute(false); !ok || got != InCall {
		t.Fatalf("unmute: got %q ok=%v, want in-call", got, ok)
	}
	// Mute toggle is idempotent-safe: mute again then end.
	if got, _ := s.Mute(true); got != Muted {
		t.Fatalf("mute again: got %q", got)
	}

	if !s.End("hangup") {
		t.Fatal("end during a call must succeed")
	}
	if s.Now() != Idle {
		t.Fatalf("after end: got %q, want idle", s.Now())
	}
	if s.Busy() {
		t.Fatal("idle must report not busy — wake words armed again")
	}
	if len(s.Endpoints()) != 0 {
		t.Fatalf("after end endpoints must be empty, got %v", s.Endpoints())
	}

	want := []State{Ringing, InCall, Muted, InCall, Muted, Idle}
	for i, w := range want {
		if transitions[i] != w {
			t.Fatalf("transition %d: got %q, want %q (all: %v)", i, transitions[i], w, transitions)
		}
	}
}

func TestRingFloorRefusedEarlyConnect(t *testing.T) {
	var now time.Time
	s := newTest(t, &now)
	s.Ring()

	if s.CanAccept() {
		t.Fatal("accept must not be offered before the ring floor")
	}
	if _, ok := s.Accept(); ok {
		t.Fatal("early connect must be refused (D-DI-3 anti-ambush)")
	}
	if s.Now() != Ringing {
		t.Fatalf("refused connect must stay ringing, got %q", s.Now())
	}

	// Just inside the floor: still refused.
	now = now.Add(RingFloor - time.Millisecond)
	if _, ok := s.Accept(); ok {
		t.Fatal("connect 1ms inside the floor must be refused")
	}

	now = now.Add(time.Millisecond)
	if _, ok := s.Accept(); !ok {
		t.Fatal("connect at exactly the floor must connect")
	}
}

func TestKillSwitchExpiry(t *testing.T) {
	var now time.Time
	s := New(50 * time.Millisecond)
	s.clock = func() time.Time { return now }
	ended := make(chan string, 1)
	s.Ended = append(s.Ended, func(reason string) { ended <- reason })

	s.Ring()
	now = now.Add(RingFloor)
	if _, ok := s.Accept(); !ok {
		t.Fatal("accept must connect")
	}
	select {
	case reason := <-ended:
		if reason != "expired" {
			t.Fatalf("kill-switch fired with %q, want expired", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("kill-switch did not fire")
	}
	if s.Now() != Idle {
		t.Fatalf("after expiry: got %q, want idle", s.Now())
	}
}

func TestKillSwitchDisabledAndRingingNotArmed(t *testing.T) {
	var now time.Time
	s := New(0) // kill-switch disabled
	s.clock = func() time.Time { return now }
	ended := make(chan string, 1)
	s.Ended = append(s.Ended, func(reason string) { ended <- reason })
	s.Ring()
	now = now.Add(RingFloor)
	if _, ok := s.Accept(); !ok {
		t.Fatal("accept must connect")
	}
	// With the kill-switch disabled nothing must self-end the call even
	// after far longer than any sane budget.
	select {
	case <-time.After(30 * time.Millisecond):
	case reason := <-ended:
		t.Fatalf("disabled kill-switch fired: %q", reason)
	}
}

func TestEndWhileRinging(t *testing.T) {
	s := New(time.Minute)
	s.Ring()
	if !s.End("hangup") {
		t.Fatal("end while ringing must succeed")
	}
	if s.CanAccept() {
		t.Fatal("after ringing ended, accept must be off the table")
	}
	if s.End("hangup") {
		t.Fatal("end while idle must be refused")
	}
}

func TestEndpointsAreAList(t *testing.T) {
	var now time.Time
	s := newTest(t, &now)
	s.Ring()
	if s.AddParticipant("third") {
		t.Fatal("add-participant while ringing must be refused")
	}
	now = now.Add(RingFloor)
	s.Accept()

	got := s.Endpoints()
	if len(got) != 2 {
		t.Fatalf("v1 call starts with two endpoints, got %v", got)
	}
	if !s.AddParticipant("third") {
		t.Fatal("add-participant during a call must succeed")
	}
	if s.Now() != Joined {
		t.Fatalf("after join: got %q, want joined", s.Now())
	}
	if got := s.Endpoints(); len(got) != 3 {
		t.Fatalf("endpoint list must grow additively, got %v", got)
	}
	if !s.AddParticipant("third") {
		t.Fatal("duplicate join must still succeed (idempotent)")
	}
	if got := s.Endpoints(); len(got) != 3 {
		t.Fatalf("duplicate join must not grow the list, got %v", got)
	}
	// Mute works in the joined state too.
	if got, ok := s.Mute(true); !ok || got != Muted {
		t.Fatalf("mute in joined: got %q ok=%v", got, ok)
	}
	if !s.End("hangup") {
		t.Fatal("end must work from joined")
	}
}

func TestEndpointListNeverHardcodedTwo(t *testing.T) {
	var now time.Time
	s := newTest(t, &now)
	s.Ring()
	now = now.Add(RingFloor)
	s.Accept()
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if !s.AddParticipant(id) {
			t.Fatalf("add-participant %q refused", id)
		}
	}
	if got := s.Endpoints(); len(got) != 7 {
		t.Fatalf("endpoint list is a list, not a pair: %v", got)
	}
}