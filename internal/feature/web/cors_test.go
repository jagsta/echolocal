package web

// Card 12 tests: the CORS middleware's fail-closed behaviour on /call/*
// (preflight headers on allowlisted Origin, no ACAO for unlisted Origin,
// pass-through when no Origin) plus config parsing (empty=deny, malformed
// rejected).

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ygelfand/echolocal/internal/config"
)

const testOrigin = "https://homeassistant.jagbains.org:8123"

func corsAllow(t *testing.T, origins []string) {
	t.Helper()
	config.Use(t.TempDir() + "/config.json")
	t.Cleanup(func() { _ = config.Set().Intercom().CORSOrigins(nil) })
	if err := config.Set().Intercom().CORSOrigins(origins); err != nil {
		t.Fatalf("set cors_origins: %v", err)
	}
}

func TestCORSPreflightAllowed(t *testing.T) {
	corsAllow(t, []string{testOrigin})
	h := CORS("POST /call/offer", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodOptions, "/call/offer", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	for _, c := range []struct{ k, want string }{
		{"Access-Control-Allow-Origin", testOrigin},
		{"Access-Control-Allow-Methods", "POST, OPTIONS"},
		{"Access-Control-Allow-Headers", "Content-Type"},
		{"Vary", "Origin"},
		{"Access-Control-Max-Age", "600"},
	} {
		if got := rec.Header().Get(c.k); got != c.want {
			t.Errorf("%s = %q, want %q", c.k, got, c.want)
		}
	}
}

func TestCORSPOSTGetsAllowOrigin(t *testing.T) {
	corsAllow(t, []string{testOrigin})
	var served bool
	h := CORS("POST /call/answer", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/call/answer", nil)
	req.Header.Set("Origin", testOrigin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !served {
		t.Fatal("POST not routed to handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != testOrigin {
		t.Errorf("Allow-Origin = %q, want %q", got, testOrigin)
	}
}

func TestCORSOriginNotInList(t *testing.T) {
	corsAllow(t, []string{testOrigin})
	var served bool
	h := CORS("POST /call/offer", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
	}))
	req := httptest.NewRequest(http.MethodOptions, "/call/offer", nil)
	req.Header.Set("Origin", "https://evil.example:8123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("disallowed preflight status = %d, want 403", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("ACAO set for non-allowlisted Origin")
	}
	req2 := httptest.NewRequest(http.MethodPost, "/call/offer", nil)
	req2.Header.Set("Origin", "https://evil.example:8123")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if !served {
		t.Error("actual POST blocked server-side (browser enforces, not us)")
	}
	if rec2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("ACAO set for non-allowlisted Origin on POST")
	}
}

func TestCORSNoOriginPassThrough(t *testing.T) {
	corsAllow(t, []string{testOrigin})
	var served bool
	h := CORS("POST /call/end", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/call/end", nil))
	if !served {
		t.Fatal("no-Origin request not passed through (Dot-to-Dot broken)")
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("ACAO set without an Origin header")
	}
}

func testCORSEmptyListDenies(t *testing.T) {
	corsAllow(t, nil)
	h := CORS("POST /call/offer", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	req := httptest.NewRequest(http.MethodOptions, "/call/offer", nil)
	req.Header.Set("Origin", testOrigin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("empty allowlist preflight status = %d, want 403 (fail-closed)", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("ACAO set with empty allowlist — fail-closed violated")
	}
}

func TestCORSMiddlewareSkippedOnOtherRoutes(t *testing.T) {
	corsAllow(t, []string{testOrigin})
	var hit bool
	h := CORS("GET /status", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodOptions, "/status", nil)
	req.Header.Set("Origin", testOrigin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !hit {
		t.Errorf("non-/call route intercepted or not routed: code=%d hit=%v", rec.Code, hit)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS applied outside /call/* routes")
	}
}

func TestParseCORSOriginsText(t *testing.T) {
	P := config.ParseCORSOriginsText
	if out, err := P(""); err != nil || out != nil {
		t.Errorf("empty input: out=%v err=%v, want nil/nil", out, err)
	}
	out, err := P(" https://a.example:8123 , http://b.example ")
	if err != nil || len(out) != 2 {
		t.Fatalf("valid list: out=%v err=%v", out, err)
	}
	for _, bad := range []string{"*", "https://*", "a.example:8123", "ftp://a.example", "https://a*b", "https://a//b"} {
		if _, err := P(bad); err == nil {
			t.Errorf("malformed entry %q accepted", bad)
		}
	}
}
