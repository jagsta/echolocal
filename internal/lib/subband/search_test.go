package subband

import (
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"testing"
)

func TestSearchLayout(t *testing.T) {
	weights, window := os.Getenv("ECHOLOCAL_FBF"), os.Getenv("ECHOLOCAL_WINDOW")
	if weights == "" || window == "" {
		t.Skip("set ECHOLOCAL_FBF and ECHOLOCAL_WINDOW")
	}

	base := geometry{
		Bands:     envInt("ECHOLOCAL_BANDS", 128),
		FFTLen:    envInt("ECHOLOCAL_FFT", 256),
		Hop:       envInt("ECHOLOCAL_HOP", 128),
		WindowLen: envInt("ECHOLOCAL_WINDOWLEN", 768),
		Beams:     envInt("ECHOLOCAL_BEAMS", 8),
		Taps:      4,
		perGroup:  4,
		margin:    1,
		boostDB:   7.2,
	}
	base.groups = alone(base.Beams)

	win, err := readFloats(window, base.WindowLen)
	if err != nil {
		t.Fatal(err)
	}
	nmics := envInt("ECHOLOCAL_MICS", 4)
	values, err := readFloats(weights, base.Bands*base.Beams*base.Taps*nmics*2)
	if err != nil {
		t.Fatal(err)
	}

	maps := map[string][]int{"all": ring(nmics)}
	if nmics < arrayMics {
		maps = map[string][]int{}
		for _, pick := range orderedPicks(arrayMics, nmics) {
			maps[fmt.Sprint(pick)] = pick
		}
	}
	t.Logf("%d microphone orderings", len(maps))

	type result struct {
		how  string
		won  float64
		beam []int
	}
	var best []result

	for _, order := range []string{"btm", "bmt", "tbm", "tmb", "mbt", "mtb"} {
		for _, down := range []bool{true, false} {
			for _, split := range []bool{true, false} {
				for name, mics := range maps {
					g := base
					g.mics = mics

					w := &Weights{g: g, window: win}
					if !fill(w, values, order, down, split) {
						continue
					}
					won, beams := steers(w)
					best = append(best, result{
						how: fmt.Sprintf(
							"%s down=%-5v split=%-5v mics=%s",
							order,
							down,
							split,
							name,
						),
						won:  won,
						beam: beams,
					})
				}
			}
		}
	}

	sort.Slice(best, func(i, j int) bool { return best[i].won > best[j].won })
	for _, r := range best[:min(12, len(best))] {
		t.Logf("%5.3f   %s   %v", r.won, r.how, r.beam)
	}
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return def
}

func ring(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func orderedPicks(n, k int) [][]int {
	if k == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, rest := range orderedPicks(n, k-1) {
		for m := range n {
			if slices.Contains(rest, m) {
				continue
			}
			out = append(out, append(append([]int{}, rest...), m))
		}
	}
	return out
}

func fill(w *Weights, values []float32, order string, down, split bool) bool {
	g := w.g
	size := map[byte]int{'b': g.Beams, 't': g.Taps, 'm': g.Inputs()}
	w.fbf = make([]complex64, g.Bands*g.Beams*g.Taps*g.Inputs())

	at := 0
	for group := range g.Bands / g.perGroup {
		for i0 := range size[order[0]] {
			for i1 := range size[order[1]] {
				for i2 := range size[order[2]] {
					pos := map[byte]int{order[0]: i0, order[1]: i1, order[2]: i2}
					beam, tap, mic := pos['b'], pos['t'], pos['m']
					if down {
						tap = g.Taps - 1 - tap
					}

					if at+2*g.perGroup > len(values) {
						return false
					}
					block := values[at : at+2*g.perGroup]
					at += 2 * g.perGroup

					for b := range g.perGroup {
						re, im := block[b], block[g.perGroup+b]
						if !split {
							re, im = block[2*b], block[2*b+1]
						}
						w.weight(group*g.perGroup+b, beam, tap)[mic] = complex(re, im)
					}
				}
			}
		}
	}
	return at == len(values)
}

func steers(w *Weights) (float64, []int) {
	var contrast float64
	seen := map[int]bool{}
	var order []int

	for step := range 12 {
		bearing := float64(step) * 30 * math.Pi / 180

		frame := planeWave(bearing, 16*testFrame)
		for len(frame) < w.g.Channels() {
			frame = append(frame, make([]int16, 16*testFrame))
		}

		b := w.New()
		b.Mix(frame)

		loudest, total := 0, float64(0)
		for j, e := range b.energy {
			total += float64(e)
			if e > b.energy[loudest] {
				loudest = j
			}
		}
		if total > 0 {
			contrast += float64(b.energy[loudest]) / (total / float64(len(b.energy)))
		}
		seen[loudest] = true
		order = append(order, loudest)
	}
	return float64(len(seen)) + contrast/1000, order
}
