package led

import (
	"testing"
	"time"
)

// TestBreatheWithBlackBaseIsInvisible pins the 15 Oct live defect ("no LED
// pulse during active call", card t_0964add2): Pulse is catalogued WITHOUT a
// palette, so effect() builds Palette{base}; a zero base renders a breathing
// frame of black — every sample 0x000000 — i.e. a claim that draws nothing.
// Any Pulse caller passing a zero Color must expect an invisible ring; the
// intercom wiring now passes light.Get().Base() like every other caller.
//
// This lives in the led package because it is a property of the effect
// catalogue + palette defaulting, not of the intercom wiring.
func TestBreatheWithBlackBaseIsInvisible(t *testing.T) {
	frame := breathe(Palette{Color{}}) // exactly what Play("Pulse", Color{}) produces

	// Walk an entire breathe period sampling finely: every frame must be
	// fully black. If this ever stops holding, the intercom's old call to
	// Play("Pulse", led.Color{}) was in fact visible and the defect had a
	// different cause — say so rather than keeping a stale comment.
	const period = 1200 * time.Millisecond
	const steps = 48
	for i := 0; i < steps; i++ {
		elapsed := time.Duration(float64(period) * float64(i) / float64(steps))
		for _, c := range frame(elapsed) {
			if c != (Color{}) {
				t.Fatalf("Pulse with a zero base showed %+v at elapsed %s — the black-frame premise is false", c, elapsed)
			}
		}
	}
}

// TestBreatheWithBaseColourIsVisible is the companion: with a non-zero base
// (as light.Get().Base() supplies) the same effect paints non-black frames —
// the fix's premise, and a guard that Pulse itself still animates.
func TestBreatheWithBaseColourIsVisible(t *testing.T) {
	base := Color{R: 0xC0, G: 0xB0, B: 0xA0}
	frame := breathe(Palette{base})

	var sawLit bool
	const period = 1200 * time.Millisecond
	for i := 0; i < 60; i++ {
		elapsed := time.Duration(float64(period) * float64(i) / 60)
		for _, c := range frame(elapsed) {
			if c != (Color{}) {
				sawLit = true
			}
		}
	}
	if !sawLit {
		t.Fatal("Pulse with a non-zero base never lit any segment")
	}
}

// TestEffectDefaultsBaseIntoPalette pins the mechanism end-to-end through the
// catalogue: Pulse carries NO palette, so effect() substitutes Palette{base};
// with a zero base every sample of the frame is black, with a lit base it
// is not. This is the exact seam that made the in-call Pulse invisible.
func TestEffectDefaultsBaseIntoPalette(t *testing.T) {
	e, ok := byName[EffectPulse]
	if ok && len(e.Palette) != 0 {
		t.Skipf("Pulse gained its own palette (%v) — the black-base premise no longer holds for it", e.Palette)
	}

	black, err := effect(EffectPulse, Color{}, Room{})
	if err != nil {
		t.Fatalf("effect(Pulse, zero base): %v", err)
	}
	for i := 0; i < 60; i++ {
		for _, c := range black(time.Duration(i) * 25 * time.Millisecond) {
			if c != (Color{}) {
				t.Fatalf("Pulse with a zero base produced %+v — premise false", c)
			}
		}
	}

	lit, err := effect(EffectPulse, Color{R: 0xC0, G: 0x40, B: 0x40}, Room{})
	if err != nil {
		t.Fatalf("effect(Pulse, lit base): %v", err)
	}
	var sawLit bool
	for i := 0; i < 60; i++ {
		for _, c := range lit(time.Duration(i) * 25 * time.Millisecond) {
			if c != (Color{}) {
				sawLit = true
			}
		}
	}
	if !sawLit {
		t.Fatal("Pulse with a lit base never lit any segment")
	}
}
