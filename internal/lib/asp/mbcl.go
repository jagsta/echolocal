package asp

import (
	"fmt"
	"math"
	"time"
)

const attack = 2 * time.Millisecond

const floorDB = -120

type mbclState struct {
	in     float32
	split  *crossover
	bands  []*bandState
	fullIn float32
	full   *limiter
	work   [][]float32
}

type bandState struct {
	compIn        float64
	compThreshDB  float64
	compRatio     float64
	compGainMinDB float64
	limIn         float32

	env    float64
	relCo  float64
	gainDB float64
	attack float64
	relax  float64
	gain   float64
	step   float64
	left   int

	lim *limiter
}

const stride = 32

const lookahead = 2 * time.Millisecond

type limiter struct {
	ceiling float64
	gain    float64
	attack  float64
	relCo   float64

	delay []float32
	want  []float64
	front int
	low   []int
	head  int
	tail  int
}

func (l *limiter) pushLow(i int, want float64) {
	for l.head != l.tail {
		back := (l.tail - 1 + len(l.low)) % len(l.low)
		if l.want[l.low[back]] < want {
			break
		}
		l.tail = back
	}
	l.low[l.tail] = i
	l.tail = (l.tail + 1) % len(l.low)
}

func newMBCL(name string, m mbcl, rate int) (*mbclState, error) {
	if !m.PreFilterBypass {
		return nil, fmt.Errorf("asp: %s wants a prefilter we do not have", name)
	}

	s := &mbclState{
		in:     float32(gainOf(m.InVol)),
		split:  newCrossover(m.Crossovers, rate),
		fullIn: float32(gainOf(m.Full.LimInVol)),
		full:   newLimiter(m.Full.LimThresh, millis(m.Full.LimRelease), rate),
	}

	for _, b := range m.Bands {
		s.bands = append(s.bands, &bandState{
			compIn:        gainOf(b.CompInVol),
			compThreshDB:  b.CompThresh,
			compRatio:     max(b.CompRatio, 1),
			compGainMinDB: b.CompGainMin,
			limIn:         float32(gainOf(b.LimInVol)),
			relCo:         smoothing(millis(b.LimRelease), rate),
			attack:        strided(smoothing(attack, rate)),
			relax:         strided(smoothing(millis(b.LimRelease), rate)),
			gain:          1,
			lim:           newLimiter(b.LimThresh, millis(b.LimRelease), rate),
		})
	}
	return s, nil
}

func newLimiter(threshDB float64, rel time.Duration, rate int) *limiter {
	n := max(int(lookahead.Seconds()*float64(rate)), 1)
	return &limiter{
		ceiling: math.Pow(10, threshDB/20),
		gain:    1,
		attack:  1 - math.Exp(-4/float64(n)),
		relCo:   smoothing(rel, rate),
		delay:   make([]float32, n),
		want:    make([]float64, n),
		low:     make([]int, n+1),
	}
}

func smoothing(d time.Duration, rate int) float64 {
	if d <= 0 {
		return 1
	}
	return 1 - math.Exp(-1/(d.Seconds()*float64(rate)))
}

func strided(co float64) float64 { return 1 - math.Pow(1-co, stride) }

func millis(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

func gainOf(db float64) float64 { return math.Pow(10, db/20) }

func (s *mbclState) reset() {
	s.split.reset()
	for _, b := range s.bands {
		b.env, b.gainDB, b.gain, b.step, b.left = 0, 0, 1, 0, 0
		b.lim.reset()
	}
	s.full.reset()
}

func (l *limiter) reset() {
	l.gain, l.front, l.head, l.tail = 1, 0, 0, 0
	clear(l.delay)
	clear(l.want)
}

func (s *mbclState) process(x []float32) {
	if len(s.work) != len(s.bands) || len(s.work[0]) != len(x) {
		s.work = make([][]float32, len(s.bands))
		for i := range s.work {
			s.work[i] = make([]float32, len(x))
		}
	}

	for i := range x {
		x[i] *= s.in
	}
	s.split.process(x, s.work)
	for i, b := range s.bands {
		b.process(s.work[i])
	}

	for i := range x {
		var sum float32
		for _, band := range s.work {
			sum += band[i]
		}
		x[i] = sum * s.fullIn
	}
	s.full.process(x)
}

func (b *bandState) process(x []float32) {
	for i, v := range x {
		in := float64(v) * b.compIn
		b.track(in)
		if b.left == 0 {
			b.steer()
		}
		b.left--
		b.gain += b.step
		x[i] = float32(in*b.gain) * b.limIn
	}
	b.lim.process(x)
}

func (b *bandState) track(v float64) {
	a := math.Abs(v)
	if a > b.env {
		b.env = a
	} else {
		b.env += (a - b.env) * b.relCo
	}
}

func (b *bandState) steer() {
	level := float64(floorDB)
	if b.env > 0 {
		level = 20 * math.Log10(b.env)
	}

	want := 0.0
	if level > b.compThreshDB {
		want = -(level - b.compThreshDB) * (1 - 1/b.compRatio)
		want = max(want, b.compGainMinDB)
	}

	b.gainDB = approach(b.gainDB, want, b.attack, b.relax)
	b.step = (math.Pow(10, b.gainDB/20) - b.gain) / stride
	b.left = stride
}

func (l *limiter) process(x []float32) {
	n := len(l.delay)
	for i, v := range x {
		want := 1.0
		if a := math.Abs(float64(v)); a > l.ceiling {
			want = l.ceiling / a
		}

		l.want[l.front] = want
		l.pushLow(l.front, want)

		out := l.delay[l.front]
		l.delay[l.front] = v
		l.front = (l.front + 1) % n
		if l.low[l.head] == l.front {
			l.head = (l.head + 1) % len(l.low)
		}

		target := l.want[l.low[l.head]]
		if target < l.gain {
			l.gain += (target - l.gain) * l.attack
		} else {
			l.gain += (target - l.gain) * l.relCo
		}

		v := float64(out) * l.gain
		x[i] = float32(max(-l.ceiling, min(l.ceiling, v)))
	}
}

func approach(have, want, attack, release float64) float64 {
	co := release
	if want < have {
		co = attack
	}
	return have + (want-have)*co
}
