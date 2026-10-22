package led

import (
	"context"
	"fmt"
	"time"

	"github.com/ygelfand/echolocal/internal/hardware/indicate"
)

// Each frame is one 36-byte i2c write.
const FrameInterval = 40 * time.Millisecond

type Frame func(elapsed time.Duration) []Color

type Kinds uint8

const (
	KindLight Kinds = 1 << iota

	KindRoom
)

func (k Kinds) Has(want Kinds) bool { return k&want != 0 }

type Room = indicate.Room

type Effect struct {
	Name string

	Palette Palette

	Kinds Kinds

	New func(p Palette) Frame

	Senses func(p Palette, r Room) Frame
}

const (
	EffectPulse        = "Pulse"
	EffectHeartbeat    = "Heartbeat"
	EffectRipple       = "Ripple"
	EffectStandingWave = "Standing Wave"
	EffectTwinkle      = "Twinkle"

	EffectCrimsonHeartbeat = "Crimson Heartbeat"
	EffectAuroraPulse      = "Aurora Pulse"
	EffectCandle           = "Candle"
	EffectFireplace        = "Fireplace"
	EffectEmbers           = "Embers"
	EffectAurora           = "Aurora"
	EffectSunsetDrift      = "Sunset Drift"
	EffectOceanRipple      = "Ocean Ripple"
	EffectRainbowTwinkle   = "Rainbow Twinkle"
	EffectForestTwinkle    = "Forest Twinkle"

	EffectComet    = "Comet"
	EffectChase    = "Chase"
	EffectScanner  = "Scanner"
	EffectPinwheel = "Pinwheel"
	EffectSpiral   = "Spiral"
	EffectWipe     = "Wipe"
	EffectHelix    = "Helix"
	EffectOrbits   = "Orbits"
	EffectBounce   = "Bounce"
	EffectSpring   = "Spring"

	EffectAlert  = "Alert"
	EffectBeacon = "Beacon"

	// EffectBlueChase is Alexa's stock wake/listen sweep: a blue dot chase
	// (palette-driven, so the claim's base colour is ignored — see
	// effect() — which is exactly what we want for a fixed Alexa colour).
	EffectBlueChase = "Alexa Listen"

	// EffectDropIn is Alexa's stock drop-in ring sweep: Beacon's rotating
	// beam locked to the green palette (Beacon itself is palette-locked to
	// alarm red, so the base colour cannot recolour it).
	EffectDropIn = "Drop-in"

	// EffectGreenPulse is the drop-in in-call breathe locked to Alexa's green
	// palette (Pulse itself takes the claim's base colour).
	EffectGreenPulse = "Drop-in Pulse"

	// EffectAnnounce is the ANNOUNCE playback indication (D-ANN-11): the same
	// breathe locked to Alexa's purple palette — visually the announcement's
	// own colour, deliberately distinct from the call lane's green.
	EffectAnnounce = "Announce"

	EffectFollower    = "Room Compass"
	EffectBeamCompass = "Beam Compass"
	EffectRoomGlow    = "Room Glow"
	EffectRoomMeter   = "Room Meter"
	EffectRoomOcean   = "Room Ocean"
	EffectRoomVU      = "Room VU"
	EffectRoomFire    = "Room Fire"
	EffectRoomSpin    = "Room Spin"
	EffectRoomAurora  = "Room Aurora"
	EffectRoomTwinkle = "Room Twinkle"
	EffectRoomEmbers  = "Room Embers"

	EffectRainbow         = "Rainbow"
	EffectFireComet       = "Fire Comet"
	EffectIceComet        = "Ice Comet"
	EffectRainbowChase    = "Rainbow Chase"
	EffectIceScanner      = "Ice Scanner"
	EffectRainbowPinwheel = "Rainbow Pinwheel"
	EffectSunsetSpiral    = "Sunset Spiral"
	EffectRainbowOrbits   = "Rainbow Orbits"
	EffectDNA             = "DNA"
	EffectPacMan          = "Pac-Man"
)

var (
	effects []Effect
	byName  = map[string]Effect{}
)

var catalogue = []struct {
	kinds   Kinds
	entries []Effect
}{
	{KindLight, ambientEffects},
	{KindLight, motionEffects},
	{KindLight, alertEffects},
	{KindRoom, roomEffects},
}

func init() {
	for _, list := range catalogue {
		for _, e := range list.entries {
			if e.Kinds == 0 {
				e.Kinds = list.kinds
			}
			effects = append(effects, e)
		}
	}
	for _, e := range effects {
		byName[e.Name] = e
	}
}

func Names(kinds Kinds) []string {
	out := make([]string, 0, len(effects))
	for _, e := range effects {
		if e.Kinds.Has(kinds) {
			out = append(out, e.Name)
		}
	}
	return out
}

func EffectNames() []string { return Names(KindLight) }

func effect(name string, base Color, room Room) (Frame, error) {
	e, ok := byName[name]
	if !ok {
		return nil, fmt.Errorf("led: no effect %q", name)
	}

	p := e.Palette
	if len(p) == 0 {
		p = Palette{base}
	}

	if e.Senses != nil {
		if room.Level == nil {
			return nil, fmt.Errorf(
				"led: effect %q reacts to the room and was given nothing to react to",
				name,
			)
		}
		return e.Senses(p, room), nil
	}
	return e.New(p), nil
}

func RunEffect(
	ctx context.Context,
	r *Ring,
	name string,
	base Color,
	room Room,
	under Frame,
) error {
	frame, err := effect(name, base, room)
	if err != nil {
		return err
	}
	return play(ctx, r, 0, through(frame, under))
}

func through(frame, under Frame) Frame {
	if under == nil {
		return frame
	}

	return func(elapsed time.Duration) []Color {
		out := frame(elapsed)
		for _, c := range out {
			if c != (Color{}) {
				return out
			}
		}
		return under(elapsed)
	}
}

func RunEffectReversed(
	ctx context.Context,
	r *Ring,
	name string,
	base Color,
	room Room,
	under Frame,
) error {
	frame, err := effect(name, base, room)
	if err != nil {
		return err
	}
	return play(ctx, r, 0, through(reverse(frame), under))
}

func reverse(frame Frame) Frame {
	return func(elapsed time.Duration) []Color {
		in := frame(elapsed)
		out := make([]Color, len(in))
		for i, c := range in {
			out[(len(in)-i)%len(in)] = c
		}
		return out
	}
}

func play(ctx context.Context, r *Ring, d time.Duration, frame Frame) error {
	t := time.NewTicker(FrameInterval)
	defer t.Stop()

	start := time.Now()
	for {
		elapsed := time.Since(start)
		if d > 0 && elapsed >= d {
			return nil
		}
		if err := r.SetSegments(frame(elapsed)); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
