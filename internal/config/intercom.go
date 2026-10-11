package config

import "github.com/ygelfand/echolocal/internal/defaults"

// Intercom holds the drop-in intercom session layer's device-side settings
// (D-DI-5, D-DI-6): the LED effects shown on ring and while in a call, and the
// max-duration kill-switch (garbage collector against forgotten open lines).
type Intercom struct {
	Ring string `json:"ring_effect"`
	Call string `json:"call_effect"`

	MaxMinutes int `json:"max_minutes"`
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