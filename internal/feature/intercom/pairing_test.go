package intercom

// Card 15: settings-table-level tests for the phone_keys and cors_origins
// ctl rows — verify the Write hooks parse + persist exactly what the
// runtime consumers (internal/feature/web/cors.go, call pairing gate) read,
// that malformed entries are rejected at SET time, and that the empty
// string clears to nil (fail-closed), not a [""] sentinel.

import (
	"testing"

	setting "github.com/ygelfand/libcountertop/pkg/settings"

	"github.com/ygelfand/echolocal/internal/config"
)

func corsRow() setting.Setting[config.Intercom] {
	for _, s := range PairingTable().Rows() {
		if s.Name == "cors_origins" {
			return s
		}
	}
	return setting.Setting[config.Intercom]{}
}

func phoneKeysRow() setting.Setting[config.Intercom] {
	for _, s := range PairingTable().Rows() {
		if s.Name == "phone_keys" {
			return s
		}
	}
	return setting.Setting[config.Intercom]{}
}

func TestCorsOriginsRowRoundTrip(t *testing.T) {
	row := corsRow()
	if row.Name != "cors_origins" {
		t.Fatalf("cors_origins row not registered in PairingTable")
	}
	var c config.Intercom
	if err := row.Write(&c, "https://home.local:8123, http://other:80"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := row.Read(&c)
	want := "https://home.local:8123,http://other:80"
	if got != want {
		t.Fatalf("read back = %q, want %q", got, want)
	}
	if len(c.CORSOrigins) != 2 || c.CORSOrigins[0] != "https://home.local:8123" {
		t.Fatalf("struct state = %v", c.CORSOrigins)
	}
}

func TestCorsOriginsRowEmptyClearsToNil(t *testing.T) {
	row := corsRow()
	var c config.Intercom
	c.CORSOrigins = []string{"https://x.test"}
	if err := row.Write(&c, ""); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if c.CORSOrigins != nil {
		t.Fatalf("empty must clear to nil (fail-closed), got %v", c.CORSOrigins)
	}
}

func TestCorsOriginsRowRejectsMalformed(t *testing.T) {
	row := corsRow()
	var c config.Intercom
	bad := []string{
		"home.local",          // no scheme
		"*,https://x.test",    // wildcard
		"https://*,https://y", // wildcard host
		"ftp://x.test",        // bad scheme
		"://x.test",           // empty scheme
		"  garbage  ",         // whitespace garbage
	}
	for _, v := range bad {
		if err := row.Write(&c, v); err == nil {
			t.Fatalf("write %q: expected error", v)
		}
	}
	if len(c.CORSOrigins) != 0 {
		t.Fatalf("rejected writes must not mutate state, got %v", c.CORSOrigins)
	}
}

func TestPhoneKeysRowRoundTripAndClear(t *testing.T) {
	row := phoneKeysRow()
	if row.Name != "phone_keys" {
		t.Fatalf("phone_keys row not registered in PairingTable")
	}
	var c config.Intercom
	if err := row.Write(&c, "jag:S3cret,hannah:L4ter"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if c.PhoneKeys["jag"] != "S3cret" || c.PhoneKeys["hannah"] != "L4ter" {
		t.Fatalf("map = %v", c.PhoneKeys)
	}
	if got, want := row.Read(&c), "hannah:L4ter,jag:S3cret"; got != want { // serialized name-sorted
		t.Fatalf("read = %q, want %q", got, want)
	}
	if err := row.Write(&c, ""); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if len(c.PhoneKeys) != 0 {
		t.Fatalf("empty must clear pairs (fail-closed), got %v", c.PhoneKeys)
	}
}

func TestPhoneKeysRowRejectsMalformed(t *testing.T) {
	row := phoneKeysRow()
	var c config.Intercom
	for _, v := range []string{"noseparator", "a:", ":b", "a:,b:c"} {
		if err := row.Write(&c, v); err == nil {
			t.Fatalf("write %q: expected error", v)
		}
	}
	if len(c.PhoneKeys) != 0 {
		t.Fatalf("rejected writes must not mutate state, got %v", c.PhoneKeys)
	}
}
