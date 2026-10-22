package subband

import (
	"math"

	"github.com/ygelfand/libcountertop/pkg/audio/fft"
)

type Beamformer struct {
	w     *Weights
	g     geometry
	scale float32

	an  []*analysis
	syn *synthesis

	bands []complex64
	at    int

	beams  [][]complex64
	merged []complex64
	energy []float32

	power   []float32
	floor   []float32
	history [][]float32
	filled  int
	slot    int

	using, held int

	carry [][]float32
	pcm   []float32
	out   []int16
}

func (w *Weights) New() *Beamformer {
	g := w.g
	f := fft.New(g.FFTLen)

	b := &Beamformer{
		w:       w,
		g:       g,
		scale:   float32(math.Pow(10, g.boostDB/20)) / bankGain(g, w.window),
		an:      make([]*analysis, g.Inputs()),
		syn:     newSynthesis(g, w.window, f),
		bands:   make([]complex64, g.Taps*g.Inputs()*g.Bands),
		beams:   make([][]complex64, g.Beams),
		merged:  make([]complex64, g.Bands),
		energy:  make([]float32, g.Beams),
		power:   make([]float32, g.Beams),
		floor:   make([]float32, g.Beams),
		history: make([][]float32, g.Beams),
		carry:   make([][]float32, g.Inputs()),
		pcm:     make([]float32, g.Hop),
	}
	for m := range b.an {
		b.an[m] = newAnalysis(g, w.window, f)
	}
	for j := range g.Beams {
		b.beams[j] = make([]complex64, g.Bands)
		b.history[j] = make([]float32, noiseFrames)
	}
	return b
}

func (b *Beamformer) frameBands(tap, mic int) []complex64 {
	at := (tap*b.g.Inputs() + mic) * b.g.Bands
	return b.bands[at : at+b.g.Bands]
}

func (b *Beamformer) tapBands(tap int) []complex64 {
	n := b.g.Inputs() * b.g.Bands
	return b.bands[tap*n : (tap+1)*n]
}

// On Fire OS 6 four of the seven microphones reach the beamformer.
func (b *Beamformer) Mix(mics [][]int16) []int16 {
	if len(mics) < b.g.Channels() {
		return nil
	}
	b.out = b.out[:0]
	b.feed(mics, b.speak)
	return b.out
}

func (b *Beamformer) Look(mics [][]int16) {
	if len(mics) < b.g.Channels() {
		return
	}
	b.feed(mics, nil)
}

func (b *Beamformer) feed(mics [][]int16, after func()) {
	for m, ch := range b.g.mics {
		for _, s := range mics[ch] {
			b.carry[m] = append(b.carry[m], float32(s))
		}
	}

	at := 0
	for len(b.carry[0])-at >= b.g.Hop {
		for m := range b.g.Inputs() {
			b.an[m].push(b.carry[m][at:at+b.g.Hop], b.frameBands(b.at, m))
		}
		b.frame()
		if after != nil {
			after()
		}
		at += b.g.Hop
	}
	for m := range b.g.Inputs() {
		b.carry[m] = append(b.carry[m][:0], b.carry[m][at:]...)
	}
}

func (b *Beamformer) frame() {
	g := b.g
	first, last := g.firstSpeechBand(), g.lastSpeechBand()

	for j := range g.Beams {
		var energy float32

		for band := range g.Bands {
			var acc complex64
			for tap := range g.Taps {
				past := b.tapBands((b.at - tap + g.Taps) % g.Taps)
				w := b.w.weight(band, j, tap)
				for m := range g.Inputs() {
					acc += w[m] * past[m*g.Bands+band]
				}
			}

			b.beams[j][band] = acc
			if band >= first && band <= last {
				energy += real(acc)*real(acc) + imag(acc)*imag(acc)
			}
		}
		b.energy[j] = energy
	}

	b.listen()
	b.steer()
	b.at = (b.at + 1) % g.Taps
}

func (b *Beamformer) speak() {
	b.merge()
	b.syn.pull(b.merged, b.pcm)

	for _, v := range b.pcm {
		b.out = append(b.out, clamp(v*b.scale))
	}
}

func (b *Beamformer) listen() {
	for j := range b.g.Beams {
		b.power[j] = signalAlpha*b.power[j] + (1-signalAlpha)*b.energy[j]
		b.history[j][b.slot] = b.power[j]
	}
	b.slot = (b.slot + 1) % noiseFrames
	b.filled = min(b.filled+1, noiseFrames)

	for j := range b.g.Beams {
		b.floor[j] = quietest(b.history[j][:b.filled])
	}
}

func quietest(history []float32) float32 {
	var least float32
	for _, p := range history {
		if p > 0 && (least == 0 || p < least) {
			least = p
		}
	}
	return least
}

func (b *Beamformer) clarity(group int) float32 {
	var signal, noise float32
	for _, j := range b.g.groups[group] {
		signal += b.power[j]
		noise += b.floor[j]
	}
	if noise <= 0 {
		return 1
	}
	return signal / noise
}

func (b *Beamformer) steer() {
	if b.held > 0 {
		b.held--
		return
	}

	clearest, best := 0, b.clarity(0)
	for group := 1; group < len(b.g.groups); group++ {
		if c := b.clarity(group); c > best {
			clearest, best = group, c
		}
	}

	if clearest != b.using && best >= b.g.margin*b.clarity(b.using) {
		b.using, b.held = clearest, b.g.hold
	}
}

func (b *Beamformer) merge() {
	group := b.g.groups[b.using]
	share := 1 / float32(len(group))
	for band := range b.merged {
		var sum complex64
		for _, j := range group {
			sum += b.beams[j][band]
		}
		b.merged[band] = sum * complex(share, 0)
	}
}

func (b *Beamformer) Beam() int {
	group := b.g.groups[b.using]
	return group[len(group)/2]
}

func clamp(v float32) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	}
	return int16(v)
}
