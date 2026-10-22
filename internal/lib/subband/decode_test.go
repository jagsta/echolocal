package subband

import (
	"fmt"
	"os"
	"sort"
	"testing"
)

func TestDecodeV2(t *testing.T) {
	weights, window := os.Getenv("ECHOLOCAL_FBF"), os.Getenv("ECHOLOCAL_WINDOW")
	if weights == "" || window == "" {
		t.Skip("set ECHOLOCAL_FBF and ECHOLOCAL_WINDOW")
	}

	base := geometry{
		Bands:     128,
		FFTLen:    256,
		Hop:       128,
		WindowLen: 768,
		Beams:     8,
		Taps:      4,
		groups:    alone(8),
		margin:    1,
		boostDB:   7.2,
	}

	win, err := readFloats(window, base.WindowLen)
	if err != nil {
		t.Fatal(err)
	}
	values, err := readFloats(weights, base.Bands*base.Beams*base.Taps*4*2)
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		how  string
		won  float64
		beam []int
	}
	var out []result

	for _, order := range []string{"emat", "meat", "maet"} {
		for _, down := range []bool{false, true} {
			for _, flip := range []bool{false} {
				for _, mics := range micOrders(t) {
					g := base
					g.mics = mics

					w := &Weights{g: g, window: win}
					if !fill4(w, values, order, down, flip) {
						continue
					}
					won, beams := steers(w)
					if won >= 6 {
						fmt.Printf(
							"  %5.3f  %s down=%v mics=%v %v\n",
							won,
							order,
							down,
							mics,
							beams,
						)
					}
					out = append(out, result{
						fmt.Sprintf("%s down=%-5v flip=%-5v mics=%v", order, down, flip, mics),
						won, beams})
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].won > out[j].won })
	for _, r := range out[:min(15, len(out))] {
		t.Logf("%6.3f   %s   %v", r.won, r.how, r.beam)
	}
}

func micOrders(t *testing.T) [][]int {
	if os.Getenv("ECHOLOCAL_ALLMICS") != "" {
		return orderedPicks(arrayMics, 4)
	}
	return [][]int{{1, 2, 4, 5}, {0, 1, 3, 4}, {0, 1, 2, 3}}
}

func fill4(w *Weights, values []float32, order string, down, flip bool) bool {
	g := w.g
	size := map[byte]int{'a': g.Bands, 'e': g.Beams, 't': g.Taps, 'm': g.Inputs()}
	w.fbf = make([]complex64, g.Bands*g.Beams*g.Taps*g.Inputs())

	at := 0
	for i0 := range size[order[0]] {
		for i1 := range size[order[1]] {
			for i2 := range size[order[2]] {
				for i3 := range size[order[3]] {
					pos := map[byte]int{order[0]: i0, order[1]: i1, order[2]: i2, order[3]: i3}
					tap := pos['t']
					if down {
						tap = g.Taps - 1 - tap
					}
					if at+2 > len(values) {
						return false
					}
					re, im := values[at], values[at+1]
					at += 2
					if flip && pos['a']%2 == 1 {
						re, im = -re, -im
					}
					w.weight(pos['a'], pos['e'], tap)[pos['m']] = complex(re, im)
				}
			}
		}
	}
	return at == len(values)
}

func TestBeamEnergiesPairUp(t *testing.T) {
	weights, window := os.Getenv("ECHOLOCAL_FBF"), os.Getenv("ECHOLOCAL_WINDOW")
	if weights == "" || window == "" {
		t.Skip("set ECHOLOCAL_FBF and ECHOLOCAL_WINDOW")
	}
	base := geometry{
		Bands:     128,
		FFTLen:    256,
		Hop:       128,
		WindowLen: 768,
		Beams:     8,
		Taps:      4,
		groups:    alone(8),
		margin:    1,
		boostDB:   7.2,
	}
	win, err := readFloats(window, base.WindowLen)
	if err != nil {
		t.Fatal(err)
	}
	values, err := readFloats(weights, base.Bands*base.Beams*base.Taps*4*2)
	if err != nil {
		t.Fatal(err)
	}

	for _, order := range []string{"tame", "taem", "mate", "maet"} {
		g := base
		g.mics = []int{1, 2, 4, 5}
		w := &Weights{g: g, window: win}
		if !fill4(w, values, order, true, false) {
			continue
		}
		b := w.New()
		b.Mix(planeWave(0, 16*testFrame))

		t.Logf("%s  energies %v", order, b.energy)
	}
}
