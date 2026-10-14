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
	return []*esphome.Action{
		{
			Name:    "dropin_dial",
			Args:    []esphome.Arg{{Name: "session"}, {Name: "caller"}},
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				s := i.session
				session := c.String("session")
				caller := c.String("caller")
				if !s.Ring() {
					return map[string]any{"state": string(s.Now())},
						fmt.Errorf("drop-in refused: dot is %s", s.Now())
				}
				slog.Info("drop-in dial", "session", session, "caller", caller)
				return map[string]any{"state": string(s.Now())}, nil
			},
		},
		{
			Name:    "dropin_connect",
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				i.CallerConnect()
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
}