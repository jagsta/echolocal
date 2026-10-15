package intercom

import (
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/echolocal/internal/feature/voice"
)

// TestDropinAnswerJoinsCallee pins the t_01e7db90 fix: HA's esphome service
// schema makes every declared action arg REQUIRED, so the callee could never
// be sent dropin_connect without a peer (and must never offer, D-P2P-5) —
// its join now rides the peerless dropin_answer action.
//
// D-P2P-8 fix (t_a6a749f2): on p2p the join opens NO ESPHome voice turn (a
// turn would open a full assist pipeline whose STT is a cloud stream), so the
// join lands in InCall with no satellite touch and no leg — the callee only
// ever answers offers.
func TestDropinAnswerJoinsCallee(t *testing.T) {
	i := &Intercom{session: New(time.Minute), status: &esphome.TextSensor{}, play: func([]int16) {}}
	i.vs = voice.Get().VS()
	if !i.session.Ring() {
		t.Fatal("ring refused")
	}
	i.CalleeJoin("di-1", "p2p")
	if i.activeLeg() != nil {
		t.Fatal("dropin_answer started an offer leg — the callee must only answer")
	}
	if i.session.Now() != InCall {
		t.Fatalf("peerless join did not join: state=%s", i.session.Now())
	}
	// Teardown still releases cleanly after a turn-less join.
	if !i.session.End("hangup") {
		t.Fatal("end refused")
	}
	// And a ended-then-recovered machine still rings fresh afterwards.
	if !i.session.Ring() {
		t.Fatal("ring refused after an ended join")
	}
}

// TestAcceptOnHarelaysStillOpensTurnAndDropsOnFailure pins the retained v1
// half: on the mothballed harelays transport the voice turn IS the audio
// transport, so join still opens it — and a failed open (no HA subscriber)
// ends the session "dropped" rather than leaving a silent in-call.
func TestAcceptOnHarelaysStillOpensTurnAndDropsOnFailure(t *testing.T) {
	i := &Intercom{session: New(time.Minute), status: &esphome.TextSensor{}, play: func([]int16) {}}
	i.vs = voice.Get().VS() // no HA subscriber: StartTurn fails
	i.setTransport(transportHARelays)
	if !i.session.Ring() {
		t.Fatal("ring refused")
	}
	i.CalleeJoin("di-1", "harelays")
	if i.activeLeg() != nil {
		t.Fatal("join created a leg")
	}
	if i.session.Now() != Idle {
		t.Fatalf("failed turn open must drop the session, got %s", i.session.Now())
	}
}

// TestDropinAnswerRefusedWhenIdle pins the fail-closed side: a peerless join
// (an HA glitch, a stale event) may not wake an idle dot into a phantom call.
func TestDropinAnswerRefusedWhenIdle(t *testing.T) {
	i := &Intercom{session: New(time.Minute), status: &esphome.TextSensor{}, play: func([]int16) {}}
	// i.vs stays nil on purpose: CallerConnect must refuse the Idle join
	// BEFORE any satellite touch, so a peerless join can never dereference
	// the satellite on a dot that was never rung.
	i.CalleeJoin("di-1", "p2p")
	if i.session.Now() != Idle {
		t.Fatalf("peerless join must be refused from idle: state=%s", i.session.Now())
	}
}
