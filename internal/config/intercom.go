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

	// CORSOrigins is the allowlist of page origins allowed to signal
	// cross-origin to /call/* via the browser (card 12). Comma-separated
	// scheme://host:port entries, no wildcards, parsed by
	// ParseCORSOriginsText. Empty/absent = NO CORS headers at all
	// (fail-closed): browser calls fail exactly as pre-card-12. Non-browser
	// Dot-to-Dot clients send no Origin and are unaffected.
	CORSOrigins []string `json:"cors_origins,omitempty"`
}

// DefaultCORSOrigin is the one known-good origin: the HA-hosted PWA page
// origin (the NPM-proxied fetch origin is a different port).
const DefaultCORSOrigin = "https://homeassistant.jagbains.org:8123"

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

// ParseCORSOriginsText parses the card-12 cors_origins allowlist from its
// comma-separated Text form. Each entry must be scheme://host[:port] —
// exactly one "//" separator, no wildcards, scheme http(s) only. Empty input
// yields nil (fail-closed: no CORS at all).
func ParseCORSOriginsText(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(v, ",") {
		o := strings.TrimSpace(part)
		scheme, rest, ok := strings.Cut(o, "://")
		if !ok || (scheme != "http" && scheme != "https") || rest == "" ||
			strings.Contains(rest, "//") || strings.ContainsAny(rest, "*?") {
			return nil, fmt.Errorf("cors_origins entry %q must be scheme://host[:port]", part)
		}
		out = append(out, o)
	}
	return out, nil
}

func defaultIntercom(d defaults.Set) Intercom {
	return Intercom{
		Ring:        DefaultRingEffect,
		Call:        DefaultCallEffect,
		MaxMinutes:  d.IntercomMaxMinutes,
		CORSOrigins: []string{DefaultCORSOrigin},
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

func (w IntercomWriter) CORSOrigins(v []string) error {
	return w.st.Update(func(c *Config) { c.Intercom.CORSOrigins = v })
}
