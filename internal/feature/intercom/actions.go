package intercom

// Card A (t_d767ce0a): the INCOMING/dial trigger. Session.Ring() existed with
// no production caller — tests only. Home Assistant's echolocal_dropin
// runtime now dials a dot through the ESPHome native API by invoking this
// user-defined Action (registered into the api Registry via
// component.Registry.Actions(); the nativeapi server lists Actions from every
// component that implements libcountertop's component.Actions interface).
//
// HA calls it as `esphome.<node>_dropin_dial` with args session/caller; the
// reply carries the dot's resulting session state so the HA caller can warn
// on busy (D-DI-6: busy-reject lives on the device, the machine never trusts
// the network for it). Teardown travels the same path: an explicit
// echolocal_dropin.hangup from HA maps session end onto this dot via
// dropin_end — accept/mute/hangup-by-button/rejection are already wired
// (wiring.go `take`, Accept, ToggleMute); only the network-initiated ring and
// end are new.
//
// Chime/LED/TextSensor are NOT re-done here: they are painted by the existing
// s.Changed subscribers in wiring.go the moment Ring() emits Idle→Ringing.

import (
	"fmt"
	"log/slog"

	esphome "github.com/ygelfand/go-esphome-device"
)

// Actions exposes the dial/end actions to Home Assistant (component.Actions).
func (i *Intercom) Actions() []*esphome.Action {
	list := []*esphome.Action{
		{
			Name:    "dropin_dial",
			Args:    []esphome.Arg{{Name: "session"}, {Name: "caller"}, {Name: "transport"}},
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				s := i.session
				session := c.String("session")
				caller := c.String("caller")
				// D-P2P-7: transport is known BEFORE the ring so the callee's
				// mic path is right from the first accepted frame — post-P2P
				// no call audio may enter the ESPHome voice turn (D-P2P-8),
				// including in the accept→connect window. Missing/unrecognised
				// values fall back to the p2p default.
				i.setTransport(transportOf(c.String("transport")))
				// t_469de14a: carry the session id INTO the machine so the offer
				// gate's freshness match (Session.OfferAuthorized(session)) is
				// armed with the id of the dial THIS dot is answering.
				if !s.RingWith(session) {
					return map[string]any{"state": string(s.Now())},
						fmt.Errorf("drop-in refused: dot is %s", s.Now())
				}
				slog.Info("drop-in dial", "session", session, "caller", caller, "transport", transportOf(c.String("transport")))
				return map[string]any{"state": string(s.Now())}, nil
			},
		},
		{
			Name: "dropin_connect",
			// D-P2P-2/5/7: peer = the OTHER dot's hostname, present only on
			// the caller (the dot that offers); transport selects the direct
			// leg (default p2p) or the mothballed harelays send-half. Extra
			// args are ignored by a pre-P2P binary, so a mixed-dot pair
			// degrades to "no audio" rather than a protocol error.
			Args:    []esphome.Arg{{Name: "session"}, {Name: "peer"}, {Name: "transport"}},
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				i.CallerConnectTo(c.String("session"), c.String("peer"), c.String("transport"))
				return map[string]any{"state": string(i.session.Now())}, nil
			},
		},
		{
			Name: "dropin_answer",
			// t_01e7db90: the CALLEE's HA-driven join under P2P. `peer` is a
			// required arg on `dropin_connect` (HA makes every declared arg
			// required), but the callee must never offer (D-P2P-5) — it only
			// answers the caller's offer on /call/dotoffer. This peerless
			// action joins the call dot-side (CallerJoin -> InCall) so the
			// session sensor paints in_call and the turn opens; the media leg
			// is created when the caller's offer arrives.
			//
			// On harelays (no arg, mothballed) this action is never invoked:
			// HA's connect fans out dropin_connect to both endpoints there.
			Args:    []esphome.Arg{{Name: "session"}, {Name: "transport"}},
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				i.CalleeJoin(c.String("session"), c.String("transport"))
				return map[string]any{"state": string(i.session.Now())}, nil
			},
		},
		{
			Name:    "dropin_end",
			Args:    []esphome.Arg{{Name: "reason"}},
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				s := i.session
				if s.Now() == Idle {
					return map[string]any{"state": string(Idle)}, nil
				}
				if !s.End(c.String("reason")) {
					return map[string]any{"state": string(s.Now())}, nil
				}
				return map[string]any{"state": string(Idle)}, nil
			},
		},
	}
	// D-ANN (announce_wiring.go): the capture/playback actions are appended
	// after the call lane's four — the call contract stays first, unchanged.
	return i.wireActionsAnnounce(list)
}
