package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ygelfand/echolocal/internal/defaults"
)

// Intercom holds the drop-in intercom session layer's device-side settings
// (D-DI-5, D-DI-6): the LED effects shown on ring and while in a call, and the
// max-duration kill-switch (garbage collector against forgotten open lines).
type Intercom struct {
	Ring string `json:"ring_effect"`
	Call string `json:"call_effect"`

	MaxMinutes int `json:"max_minutes"`

	// PhoneKeys maps each paired phone/web caller's NAME (D-DI-8c "phone:"
	// convention) to its pairing key, presented with its signalling POSTs.
	// The key IS the identity: on a constant-time match the offer's From is
	// overwritten server-side with "phone:<name>" and the body's from claim
	// is ignored (anti-spoof). Empty/absent map means pairing is OFF:
	// phone-sourced offers are rejected fail-closed (card 7/8,
	// specs/phone-dropin-webrtc.md, D-DI-9b).
	// Dot-to-Dot calls (ids without the "phone:" prefix) are unaffected.
	PhoneKeys map[string]string `json:"phone_keys,omitempty"`
}

const (
	DefaultRingEffect  = "Beacon"
	DefaultCallEffect  = "Pulse"
	DefaultMaxMinutes  = 30
	MinimumRingSeconds = 3 // D-DI-3: anti-ambush ring floor, enforced in the session state machine
)

// PhoneKeysText serializes the name->key map for the Text setting row
// ("name1:key1,name2:key2"); empty map renders "".
func PhoneKeysText(keys map[string]string) string {
	names := make([]string, 0, len(keys))
	for n := range keys {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+":"+keys[n])
	}
	return strings.Join(parts, ",")
}

// ParsePhoneKeysText parses the Text setting row back into the map.
// Entries without a ":" are rejected; empty input yields an empty map.
func ParsePhoneKeysText(v string) (map[string]string, error) {
	keys := map[string]string{}
	v = strings.TrimSpace(v)
	if v == "" {
		return keys, nil
	}
	for _, part := range strings.Split(v, ",") {
		name, key, ok := strings.Cut(strings.TrimSpace(part), ":")
		name, key = strings.TrimSpace(name), strings.TrimSpace(key)
		if !ok || name == "" || key == "" {
			return nil, fmt.Errorf("phone_keys entry %q must be name:key", part)
		}
		keys[name] = key
	}
	return keys, nil
}

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

func (w IntercomWriter) PhoneKeys(v map[string]string) error {
	return w.st.Update(func(c *Config) { c.Intercom.PhoneKeys = v })
}
