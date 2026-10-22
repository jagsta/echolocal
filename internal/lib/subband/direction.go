package subband

import (
	"math"
	"math/cmplx"
)

const bearingSteps = 360

func (w *Weights) Bearings(delay func(channel int, bearing float64) float64) []float64 {
	g := w.g
	out := make([]float64, g.Beams)
	for beam := range g.Beams {
		best := math.Inf(-1)
		for step := range bearingSteps {
			bearing := 2 * math.Pi * float64(step) / bearingSteps
			if p := w.response(beam, bearing, delay); p > best {
				best, out[beam] = p, bearing
			}
		}
	}
	return out
}

func (w *Weights) response(
	beam int,
	bearing float64,
	delay func(channel int, bearing float64) float64,
) float64 {
	g := w.g
	var power float64
	for band := g.firstSpeechBand(); band <= g.lastSpeechBand(); band++ {
		turn := -2 * math.Pi * float64(band) / float64(g.FFTLen)
		var acc complex128
		for tap := range g.Taps {
			weights := w.weight(band, beam, tap)
			for m, ch := range g.mics {
				late := delay(ch, bearing) + float64(tap*g.Hop)
				acc += complex128(weights[m]) * cmplx.Rect(1, turn*late)
			}
		}
		power += real(acc)*real(acc) + imag(acc)*imag(acc)
	}
	return power
}
