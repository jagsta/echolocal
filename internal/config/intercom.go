package config

import "github.com/ygelfand/echolocal/internal/defaults"

// Intercom holds the drop-in intercom session layer's device-side settings
// (D-DI-5, D-DI-6): the LED effects shown on ring and while in a call, and the
// max-duration kill-switch (garbage collector against forgotten open lines).
type Intercom struct {
	Ring string `json:"ring_effect"`
	Call string `json:"call_effect"`

	MaxMinutes int `json:"max_minutes"`

	// PhoneKey is the pairing key a phone/web caller (D-DI-8) must present
	// with its signalling POSTs. Empty means pairing is OFF: phone-sourced
	// offers are rejected fail-closed (card 7, specs/phone-dropin-webrtc.md).
	// Dot-to-Dot calls (ids without the "phone:" prefix) are unaffected.
	PhoneKey string `json:"phone_key,omitempty"`
}

const (
	DefaultRingEffect  = "Beacon"
	DefaultCallEffect  = "Pulse"
	DefaultMaxMinutes  = 30
	MinimumRingSeconds = 3 // D-DI-3: anti-ambush ring floor, enforced in the session state machine
)

func defaultIntercom(d defaults.Set) Intercom {
	return Intercom{
		Ring:       DefaultRingEffect,
		Call:       DefaultCallEffect,
		MaxMinutes: d.IntercomMaxMinutes,
	}
}

type IntercomWriter struct{ st *Store }

func (w IntercomWriter) Ring(v string) error {
	return w.st.Update(func(c *Config) { c.Intercom.Ring = v })
}

func (w IntercomWriter) Call(v string) error {
	return w.st.Update(func(c *Config) { c.Intercom.Call = v })
}

func (w IntercomWriter) MaxMinutes(v int) error {
	return w.st.Update(func(c *Config) { c.Intercom.MaxMinutes = v })
}

func (w IntercomWriter) PhoneKey(v string) error {
	return w.st.Update(func(c *Config) { c.Intercom.PhoneKey = v })
}
