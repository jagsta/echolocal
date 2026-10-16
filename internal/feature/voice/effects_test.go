package voice

import (
	"testing"

	led "github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"
)

// The stock Alexa colour card (LED defect card t_f902c367, live 16 Oct): the
// wake-word listen turn showed a WHITE sweep and an "answered elsewhere" wake
// word showed a PURPLE chase. Neither is stock Alexa.

// TestListenEffectRemapsUnconfiguredPulse pins the white-sweep fix: with no
// wake words configured (the deployed state, wake.words=null), the slot falls
// back to schema.DefaultWakeWord whose effect is "Pulse" — Pulse takes the
// claim base, forced white by the off ring light. The turn must play the
// palette-locked blue Alexa Listen instead.
func TestListenEffectRemapsUnconfiguredPulse(t *testing.T) {
	if got := listenEffect(schema.DefaultEffect, 0); got != led.EffectBlueChase {
		t.Fatalf("unconfigured listen turn plays %q, want %q", got, led.EffectBlueChase)
	}
}

// TestListenEffectKeepsAnExplicitEffect pins that any configured wake slot
// (Words rows present) or any explicitly chosen non-default effect is
// untouched: a user-chosen effect must win over the Alexa blue fallback.
func TestListenEffectKeepsAnExplicitEffect(t *testing.T) {
	if got := listenEffect("Beacon", 0); got != "Beacon" {
		t.Fatalf("explicit effect lost: played %q, want Beacon", got)
	}
	if got := listenEffect(schema.DefaultEffect, 1); got != schema.DefaultEffect {
		t.Fatalf("configured slot's default lost: played %q, want Pulse untouched", got)
	}
}

// TestElsewhereIsSilent pins the purple-cycle fix: "answered elsewhere" shows
// nothing (stock Alexa has no such indication); the old flash was a Chase in
// 0x80,0x40,0xC0.
func TestElsewhereIsSilent(t *testing.T) {
	d := led.NewDriver(nil)
	before := d.Busy().Running()

	(&turnEffects{}).Elsewhere()

	if after := d.Busy().Running(); after != before {
		t.Fatalf("Elsewhere started a busy indication (running=%v), want no change", after)
	}
}