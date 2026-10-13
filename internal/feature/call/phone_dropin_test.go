package call

// Card 8 (D-DI-9b, specs/phone-dropin-ux-decisions.md): key-as-identity —
// the pairing gate matches the envelope key against EACH entry of the
// phone_keys map, and the matched entry's NAME becomes the caller identity
// server-side (the body's from claim is ignored for phone sources, so a
// spoofed "from" cannot rename a caller). Fail-closed: an absent or empty
// map rejects every phone-sourced request with 403.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ygelfand/libcountertop/pkg/media/call"

	"github.com/ygelfand/echolocal/internal/config"
)

func newTestGate(t *testing.T) *phoneGate {
	t.Helper()
	return newPhoneGate(&call.Calls{})
}

func offerRequest(from, pairing string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/call/offer", nil)
	return req
}

func TestPhonePairingKeyedIdentitySpoofRejected(t *testing.T) {
	config.Use(t.TempDir() + "/config.json")
	if err := config.Set().Intercom().PhoneKeys(map[string]string{"jag": "k1"}); err != nil {
		t.Fatalf("set keys: %v", err)
	}
	defer func() { _ = config.Set().Intercom().PhoneKeys(map[string]string{}) }()

	g := newTestGate(t)

	// Spoofed "from" does NOT survive: matching key forces server-side
	// identity to the matched entry's name.
	m := &phoneEnvelope{From: "phone:eve", Pairing: "k1"}
	w := httptest.NewRecorder()
	if !g.check(w, m) {
		t.Fatalf("matching key with spoofed from must be accepted")
	}
	if m.From != "phone:jag" {
		t.Errorf("From = %q, want %q (key IS the identity)", m.From, "phone:jag")
	}

	// Unknown key rejected with 401.
	m2 := &phoneEnvelope{From: "phone:jag", Pairing: "wrong"}
	w2 := httptest.NewRecorder()
	if g.check(w2, m2) {
		t.Fatalf("mismatched key must be rejected")
	}
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w2.Code)
	}
}

func TestPhonePairingFailClosedEmptyMap(t *testing.T) {
	config.Use(t.TempDir() + "/config.json")
	// Absent/empty map: fail-closed 403 "pairing disabled".
	if err := config.Set().Intercom().PhoneKeys(nil); err != nil {
		t.Fatalf("clear keys: %v", err)
	}
	g := newTestGate(t)
	m := &phoneEnvelope{From: "phone:jag", Pairing: "k1"}
	w := httptest.NewRecorder()
	if g.check(w, m) {
		t.Fatalf("empty keys map must be fail-closed")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	// Dot-to-Dot caller untouched.
	dot := &phoneEnvelope{From: "dot-kitchen", Pairing: ""}
	wd := httptest.NewRecorder()
	if !g.check(wd, dot) {
		t.Fatalf("dot-to-dot caller must pass untouched")
	}
}