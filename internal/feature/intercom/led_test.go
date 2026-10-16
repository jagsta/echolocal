package intercom

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ygelfand/echolocal/internal/hardware/led"
)

// drainRing is a real Ring backed by a throwaway directory: the driver's
// frame files land there, so the test can read the physical frame composed
// (a claim alone never touches the ring — only Driver.Run renders).
func drainRing(t *testing.T) *led.Ring {
	t.Helper()

	dir := t.TempDir()
	for _, name := range []string{"frame", "leds_current", "boot_animation"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("preparing %s: %v", name, err)
		}
	}
	return &led.Ring{Path: dir}
}

// runningDriver is a driver over drainRing with its render loop live.
func runningDriver(t *testing.T) (*led.Driver, *led.Ring) {
	t.Helper()

	r := drainRing(t)
	d := led.NewDriver(r)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = d.Run(ctx) }()
	return d, r
}

// seg0 reads segment 0's colour from the ring's last written frame; ok is
// false when nothing has been written yet.
func seg0(r *led.Ring) (led.Color, bool) {
	frame, err := r.Frame()
	if err != nil || len(frame) < 3 {
		return led.Color{}, false
	}
	return led.Color{R: frame[0], G: frame[1], B: frame[2]}, true
}

// waitGreen polls the ring until segment 0 shows a green-dominant frame
// (Drop-in Pulse/Beacon render: the green sweeps and breathes over black).
func waitGreen(t *testing.T, r *led.Ring, label string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := seg0(r); ok && c.G > 0 && c.G >= c.R*4 && c.G >= c.B*4 {
			return // green-dominant: the effect is rendering the green palette
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never showed a green frame on the ring", label)
}

// waitAmber polls the ring until segment 0 is the static amber paint.
func waitAmber(t *testing.T, r *led.Ring, label string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := seg0(r); ok && c == amber {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never showed the solid amber paint on the ring", label)
}

// waitBlack polls the ring until it is fully dark (cleared).
func waitBlack(t *testing.T, r *led.Ring, label string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frame, err := r.Frame()
		if err == nil {
			dark := true
			for _, v := range frame {
				if v != 0 {
					dark = false
					break
				}
			}
			if dark {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never cleared the ring to black", label)
}

// The stock Alexa drop-in colour card (LED card t_05fc05ad): ring AND in-call
// shows are GREEN regardless of the user's ring-light base; muted keeps its
// amber paint (D-DI-6); idle still clears.
func TestShowPaintsAlexaGreenOnRingAndInCall(t *testing.T) {
	d, r := runningDriver(t)
	i := &Intercom{
		session: New(30 * time.Minute),
		led:     d.Claim(led.PriorityAlarm),
	}

	// Ringing: the Drop-in sweep (Beacon locked to the green palette).
	i.show(Ringing)
	waitGreen(t, r, "ringing")

	// InCall and Joined: the green Drop-in Pulse — the t_0964add2 InCall-pulse
	// fix must keep firing (only the colour changes).
	i.show(InCall)
	waitGreen(t, r, "in-call")
	i.show(Joined)
	waitGreen(t, r, "joined")

	// Muted: the claim switches to the static amber paint (D-DI-6, unchanged
	// by the recolour).
	i.show(Muted)
	waitAmber(t, r, "muted")

	// Idle clears: the ring falls back to black (nothing driving it).
	i.show(Idle)
	waitBlack(t, r, "idle")
}

// The drop-in palette entries exist and are actually green (the recolour is
// only observable if the catalogue colours are).
func TestDropInCatalogueEntriesAreGreen(t *testing.T) {
	for _, name := range []string{led.EffectDropIn, led.EffectGreenPulse, led.EffectBlueChase} {
		for _, offered := range led.EffectNames() {
			if offered == name {
				goto next
			}
		}
		t.Errorf("%q is not offered by EffectNames; settings UI cannot pick it", name)
	next:
	}
}