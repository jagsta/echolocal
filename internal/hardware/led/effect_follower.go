package led

import (
	"math"
	"time"
)

func follower(p Palette, r Room) Frame { return compass(p, r, facing) }

func beamFollower(p Palette, r Room) Frame { return compass(p, r, beam) }

func compass(p Palette, r Room, toward func(Room) (float64, bool)) Frame {
	const (
		narrow = 1.2
		wide   = 3.2

		ease = 0.18
	)

	var at float64
	var known bool

	return func(time.Duration) []Color {
		x := level(r)

		if want, ok := toward(r); ok {
			switch {
			case !known:
				at, known = want, true
			default:
				d := math.Mod(
					math.Mod(want-at, Segments)+Segments+Segments/2,
					Segments,
				) - Segments/2
				at += d * ease
			}
		}
		if !known {
			return arcOf(x, p)
		}

		out := make([]Color, Segments)
		dot(out, at, p.Shade(x, x), narrow+(wide-narrow)*x)
		return out
	}
}
