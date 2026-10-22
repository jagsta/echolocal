package led

import (
	"math"
	"time"
)

func around(p Palette, f float64) []Color {
	out := make([]Color, Segments)
	for i := range out {
		out[i] = scale(p.At(float64(i)/Segments), f)
	}
	return out
}

func shaded(p Palette, f float64) []Color {
	out := make([]Color, Segments)
	for i := range out {
		out[i] = p.Shade(f, f)
	}
	return out
}

func dot(out []Color, at float64, c Color, width float64) {
	for i := range out {
		d := ringDist(i, at)
		if d >= width {
			continue
		}
		f := 1 - d/width
		out[i] = add(out[i], scale(c, f*f))
	}
}

func ringDist(i int, at float64) float64 {
	d := math.Abs(float64(i) - math.Mod(math.Mod(at, Segments)+Segments, Segments))
	return math.Min(d, Segments-d)
}

func wander(t, offset float64) float64 {
	s := math.Sin(2*math.Pi*(1.7*t+offset)) +
		math.Sin(2*math.Pi*(2.9*t+1.3*offset)) +
		math.Sin(2*math.Pi*(4.7*t+0.6*offset))
	return (s/3 + 1) / 2
}

func hash(x uint32) uint32 {
	x ^= x >> 16
	x *= 0x7FEB352D
	x ^= x >> 15
	x *= 0x846CA68B
	x ^= x >> 16
	return x
}

func level(r Room) float64 {
	if r.Level == nil {
		return 0
	}
	return math.Max(0, math.Min(1, r.Level()))
}

func facing(r Room) (float64, bool) { return segmentOf(r.Facing) }

func beam(r Room) (float64, bool) { return segmentOf(r.Beam) }

func segmentOf(toward func() (float64, bool)) (float64, bool) {
	if toward == nil {
		return 0, false
	}

	at, ok := toward()
	if !ok {
		return 0, false
	}
	return math.Mod(math.Mod(at, 1)+1, 1) * Segments, true
}

func byRoom(build func(Palette) Frame) func(Palette, Room) Frame {
	return func(p Palette, r Room) Frame {
		frame := build(p)

		return func(elapsed time.Duration) []Color {
			x := level(r)
			out := frame(elapsed)
			for i := range out {
				out[i] = scale(out[i], x)
			}
			return out
		}
	}
}
