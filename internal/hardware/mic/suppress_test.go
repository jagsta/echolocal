package mic

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/ygelfand/libcountertop/pkg/audio/aec"

	"github.com/ygelfand/echolocal/internal/lib/audio"
)

// SUPPRESS_REPLAY is a capture from `echod ctl audio record echo`;
// SUPPRESS_REPLAY_OUT receives one mono WAV per EchoSuppressActive setting.
// Replay runs the canceller + residual suppressor over a real device take
// with the active-echo dip swept, so the settings can be compared
// apples-to-apples before anyone goes near a live default.
func TestReplaySuppressSweep(t *testing.T) {
	path, dir := os.Getenv("SUPPRESS_REPLAY"), os.Getenv("SUPPRESS_REPLAY_OUT")
	if path == "" || dir == "" {
		t.Skip("SUPPRESS_REPLAY or SUPPRESS_REPLAY_OUT not set")
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

	sweep := []struct {
		name string
		set  int
	}{{"supp-15", -15}, {"supp-25", -25}, {"supp-30", -30}, {"supp-40", -40}}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, v := range sweep {
		s := New()
		s.mixer = Center{}
		s.cancelling = true
		s.adapt.Store(true)
		var echo *aec.MDF
		if s.cancel != nil {
			echo = s.cancel.Filter(0)
		}
		p, err := aec.NewPreprocessor(aec.Block, Rate, echo)
		if err != nil {
			t.Fatal(err)
		}
		p.EchoSuppressActive = v.set
		s.suppress = p

		frames, stop := s.Listen("sweep-" + v.name)
		var got []byte
		for at := 0; at+FrameSamples <= n; at += FrameSamples {
			s.broadcast(replayRaw(mic[at:at+FrameSamples], ref[at:at+FrameSamples]))
			f := <-frames
			for _, x := range f {
				got = binary.LittleEndian.AppendUint16(got, uint16(x))
			}
		}
		stop()

		out := filepath.Join(dir, v.name+".wav")
		if err := audio.WriteWAV(out, got, Rate, 1); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s", out)
	}
}