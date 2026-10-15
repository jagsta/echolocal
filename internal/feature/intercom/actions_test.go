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
// The shared VoiceSatellite has no HA subscriber in tests, so StartTurn fails
// (the same unpaired-dot condition TestConnectWithoutPeerStartsNoLeg uses) and
// the join ends the session "dropped" back to Idle. The joins ARE pinned: the
// InCall transition is only reachable through CallerJoin, and the pytest-side
// callee-args contract plus the live E2E pin the InCall paint.
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
	// The join transition ran: the dropped teardown proves CallerJoin fired
	// (InCall is only reachable via CallerJoin) and the turn-open failure
	// teardown released cleanly — never wedged InCall.
	if i.session.Now() != Idle {
		t.Fatalf("peerless join wedged the session in %s", i.session.Now())
	}
	// And a refused-then-recovered machine still rings fresh afterwards.
	if !i.session.Ring() {
		t.Fatal("ring refused after a dropped join")
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
