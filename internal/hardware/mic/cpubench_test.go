package mic

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/ygelfand/libcountertop/pkg/audio/aec"
)

// BENCH_REPLAY: raw CPU cost of the sounding path (MDF canceller + residual
// suppressor) per 16 kHz sample at different tap counts, replayed against a
// real 3-channel take. amd64 timing is a relative ratio only; armv7 absolute
// cost scales with the same m = taps/frame FFT-block count.
func TestCancellerCPUSweep(t *testing.T) {
	path := os.Getenv("BENCH_REPLAY")
	if path == "" {
		t.Skip("BENCH_REPLAY not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pcm := b[44:]
	n := len(pcm) / 6
	mic, ref := make([]int16, n), make([]int16, n)
	for i := range n {
		mic[i] = int16(binary.LittleEndian.Uint16(pcm[6*i:]))
		ref[i] = int16(binary.LittleEndian.Uint16(pcm[6*i+2:]))
	}

	for _, taps := range []int{256, 512, 1024} {
		c, err := aec.NewCanceller(1, taps, Rate)
		if err != nil {
			t.Fatal(err)
		}
		echo := c.Filter(0)
		sup, err := aec.NewPreprocessor(aec.Block, Rate, echo)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]int16, FrameSamples)
		out := make([][]int16, 1)
		out[0] = make([]int16, FrameSamples)

		// warm
		for i := 0; i < 50; i++ {
			at := (i * FrameSamples) % (n - FrameSamples)
			copy(buf, mic[at:at+FrameSamples])
			_ = c.Process(ref[at:at+FrameSamples], [][]int16{buf}, out, nil)
			_ = sup.Run(out[0])
		}

		const iters = 60
		start := time.Now()
		for i := 0; i < iters; i++ {
			at := (i * FrameSamples) % (n - FrameSamples)
			copy(buf, mic[at:at+FrameSamples])
			_ = c.Process(ref[at:at+FrameSamples], [][]int16{buf}, out, nil)
			_ = sup.Run(out[0])
		}
		elapsed := time.Since(start)
		t.Logf("taps=%d  %.2f us per 16kHz sample  (frame budget 62.5us = %.1fx realtime)",
			taps, float64(elapsed.Microseconds())/float64(iters*FrameSamples), 62.5*float64(iters*FrameSamples)/float64(elapsed.Microseconds()))
	}
}