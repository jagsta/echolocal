package call

// Card 8 (D-DI-9b, specs/phone-dropin-ux-decisions.md): key-as-identity —
// the pairing gate matches the envelope key against EACH entry of the
// phone_keys map, and the matched entry's NAME becomes the caller identity
// server-side (the body's from claim is ignored for phone sources, so a
// spoofed "from" cannot rename a caller). Fail-closed: an absent or empty
// map rejects every phone-sourced request with 403.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ygelfand/libcountertop/pkg/media/call"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/web"
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

// Card 13: a gated phone offer must force the callee's answer POST to loop
// back to the dot itself, and the key-derived identity must replace the
// body's from claim upstream. Upstream offered() derives the callee
// session's s.addr purely from r.RemoteAddr, so the gate must rewrite it to
// 127.0.0.1:<web.Port> (the dot's own web listener) before calling upstream
// — otherwise the callee POSTs its answer to the phone/proxy (live finding:
// "step=answer err=status 404" through NPM, unreachable at the phone
// direct). offerPhone blocks on the loopback answer, so the handler runs in
// a goroutine and the test drives the answer through answerPhone.
type offersRecorder struct {
	got  *http.Request
	served chan struct{}
}

func (o *offersRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	o.got = r
	w.WriteHeader(http.StatusAccepted)
	close(o.served) // pending is registered before upstream runs (design)
}

func TestPhoneOfferForcesAnswerLoopbackRemoteAddr(t *testing.T) {
	config.Use(t.TempDir() + "/config.json")
	if err := config.Set().Intercom().PhoneKeys(map[string]string{"jag": "k1"}); err != nil {
		t.Fatalf("set keys: %v", err)
	}
	defer func() { _ = config.Set().Intercom().PhoneKeys(map[string]string{}) }()

	rec := &offersRecorder{served: make(chan struct{})}
	g := &phoneGate{offer: &call.Calls{}, upOffer: rec,
		pending: map[string]chan phoneEnvelope{}}

	body, _ := json.Marshal(phoneEnvelope{
		Call: "phone-loop1", From: "phone:kitchen-room", Name: "Kitchen",
		SDP: "offer-sdp", Pairing: "k1",
	})
	r := httptest.NewRequest(http.MethodPost, "/call/offer",
		bytes.NewReader(body))
	r.RemoteAddr = "192.168.10.83:51000" // NPM proxy, not the dot
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { g.offerPhone(w, r); close(done) }()

	// Wait until upstream (and therefore the pending registration) ran,
	// then deliver the loopback answer.
	select {
	case <-rec.served:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream offer handler never ran")
	}
	wa := httptest.NewRecorder()
	ab, _ := json.Marshal(phoneEnvelope{Call: "phone-loop1", SDP: "answer-sdp"})
	g.answerPhone(wa, httptest.NewRequest(http.MethodPost, "/call/answer",
		bytes.NewReader(ab)))
	if wa.Code != http.StatusOK {
		t.Errorf("loopback answer status = %d, want 200", wa.Code)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("offerPhone never unblocked on the loopback answer")
	}

	if rec.got == nil {
		t.Fatalf("upstream offer handler never ran")
	}
	if got := rec.got.RemoteAddr; got != "127.0.0.1:"+strconv.Itoa(web.Port) {
		t.Errorf("upstream RemoteAddr = %q, want the dot loopback %q", got,
			"127.0.0.1:"+strconv.Itoa(web.Port))
	}
	// The gate must have forced the key-derived identity upstream so the
	// ring announces the caller, not the URL's room name (live leak fix).
	var sent phoneEnvelope
	b, err := io.ReadAll(rec.got.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	if err := json.Unmarshal(b, &sent); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if sent.From != "phone:jag" || sent.Name != "jag" {
		t.Errorf("upstream From/Name = %q/%q, want phone:jag/jag (key IS identity)",
			sent.From, sent.Name)
	}
	// The original /call/offer response carries the answer SDP.
	if w.Code != http.StatusOK {
		t.Errorf("offer response status = %d, want 200", w.Code)
	}
	var held phoneEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &held); err != nil {
		t.Fatalf("decode held offer response: %v", err)
	}
	if held.SDP != "answer-sdp" {
		t.Errorf("held offer SDP = %q, want the loopback answer SDP", held.SDP)
	}
}