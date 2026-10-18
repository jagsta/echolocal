package mic

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/ygelfand/libcountertop/pkg/audio/aec"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/lib/audio"
)

// ECHO_REPLAY is a capture from `echod ctl audio record echo`; WAVs go to ECHO_REPLAY_OUT.
func TestReplayPipeline(t *testing.T) {
	path, dir := os.Getenv("ECHO_REPLAY"), os.Getenv("ECHO_REPLAY_OUT")
	if path == "" || dir == "" {
		t.Skip("ECHO_REPLAY or ECHO_REPLAY_OUT not set")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint16(b[22:]) != 3 {
		t.Fatal("want the three channel capture: mic, loopback, out")
	}
	pcm := b[44:]
	n := len(pcm) / 6
	mic, ref := make([]int16, n), make([]int16, n)
	for i := range n {
		mic[i] = int16(binary.LittleEndian.Uint16(pcm[6*i:]))
		ref[i] = int16(binary.LittleEndian.Uint16(pcm[6*i+2:]))
	}

	variants := []struct {
		name                          string
		cancel, denoise, frozen, cold bool
		taps                          int
		raw                           bool
	}{
		{"device", true, true, false, false, 0, false},
		{"no-denoise", true, false, false, false, 0, false},
		{"frozen", true, true, true, false, 0, false},
		{"frozen-no-denoise", true, false, true, false, 0, false},
		{"no-cancel", false, true, false, false, 0, false},
		{"cold", true, true, false, true, 0, false},
		{"cold-no-denoise", true, false, false, true, 0, false},
		{"raw-cold", true, false, false, true, 0, true},
		{"256", true, true, false, false, 256, false},
		{"256-cold", true, true, false, true, 256, false},
		{"512", true, true, false, false, 512, false},
		{"512-cold", true, true, false, true, 512, false},
		{"1024-cold", true, true, false, true, 1024, false},
		{"4096-cold", true, true, false, true, 4096, false},
		{"4096-raw-cold", true, false, false, true, 4096, true},
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range variants {
		s := New()
		s.mixer = Center{}
		s.cancelling = v.cancel
		s.SetLeveling(!v.raw)
		if v.taps != 0 {
			s.cancel = newCanceller(v.taps)
		}
		suppressor := func() *aec.Preprocessor {
			if !v.denoise {
				return nil
			}
			return newSuppressor(s.cancel)
		}
		s.suppress = suppressor()

		frames, stop := s.Listen("replay")
		var got []byte
		for pass := range 2 {
			if v.cold && pass == 1 {
				break
			}
			if pass == 1 {
				s.Leveler = newLeveler(config.Get().Microphone)
				s.SetLeveling(!v.raw)
				s.suppress = suppressor()
				s.Freeze(v.frozen)
			}
			for at := 0; at+FrameSamples <= n; at += FrameSamples {
				s.broadcast(replayRaw(mic[at:at+FrameSamples], ref[at:at+FrameSamples]))
				f := <-frames
				if pass == 0 && !v.cold {
					continue
				}
				for _, v := range f {
					got = binary.LittleEndian.AppendUint16(got, uint16(v))
				}
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

func replayRaw(mic, ref []int16) []byte {
	const frameBytes = Channels * Bits / 8

	raw := make([]byte, len(mic)*frameBytes)
	for f := range mic {
		off := f * frameBytes
		put24(raw[off+CenterMic*3:], int32(mic[f])<<8)
		for c := range Refs {
			put24(raw[off+(Mics+c)*3:], int32(ref[f])<<8)
		}
	}
	return raw
}
