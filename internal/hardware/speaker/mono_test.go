package speaker

import (
	"reflect"
	"testing"

	"github.com/ygelfand/libcountertop/pkg/audio/alsa"

	"github.com/ygelfand/echolocal/internal/config"
)

// laneMs is how long a run of interleaved int16 slots lasts, in milliseconds.
func laneMs(slots int) float64 {
	return float64(slots) / Channels / float64(Rate) * 1000
}

// TestPlayMonoFillsTheCodecPeriod is the F1 regression pin (review of card
// t_be5293f6): a 16 kHz MONO producer — the dot↔dot intercom leg's decoded
// Opus frame, 320 samples = 20 ms, and the mothballed HA relay's payload of
// the same encoding — must land on the mixer lane as Rate/Channels audio.
//
// The lane is raw int16 slots inside the interleaved 48 kHz stereo period:
// mix.Bus adds them verbatim (libcountertop pkg/audio/mix queue.go/bus.go),
// with no per-lane rate and no resampler anywhere downstream. So a correct
// 20 ms frame occupies 320*Rate/VoiceRate*Channels = 1920 slots = 20.000 ms;
// pushing the raw 16 kHz mono through took 320 slots = 3.333 ms (6.0x too
// fast) and only ever the left channel.
//
// The test measures what the review measured (queue the lane, render one
// period, count the slots the audio spans) — through the real PlayMono →
// PlayStream path. A non-nil device pointer is enough to get past
// PlayStream's drop-when-absent guard; it is never opened, so no ALSA.
func TestPlayMonoFillsTheCodecPeriod(t *testing.T) {
	p := &Player{}
	p.pb = &alsa.Playback{} // "device present": PlayStream pushes to the bus

	const frame = 320 // mic.FrameSamples: 20 ms at 16 kHz, one leg frame
	mono := make([]int16, frame)
	for i := range mono {
		mono[i] = 8000
	}
	p.PlayMono(config.StreamIntercom, mono)

	out := make([]int32, period*Channels)
	queued, _ := p.bus.Next(0, out)

	want := frame * Rate / VoiceRate * Channels // 1920 slots = 960 stereo frames
	if queued != want {
		t.Fatalf("lane holds %d int16 slots (%.3f ms), want %d (%.3f ms)",
			queued, laneMs(queued), want, laneMs(want))
	}
	if got := laneMs(queued); got < 19.99 || got > 20.01 {
		t.Fatalf("frame lasts %.3f ms, want 20.000 ms", got)
	}

	// Both channels carry the frame: interleaved pairs are equal and the
	// signal spans (essentially) the whole queued region — not just its first
	// 320 slots, which is where the raw-push bug stopped. The first/last
	// samples may round to zero in the resampler's transient, hence the small
	// tolerance against the exact pair count.
	pairs, span := 0, 0
	for i := 0; i < queued; i += Channels {
		if out[i+1] != out[i] {
			t.Fatalf("stereo pair %d: %d/%d — channels must match", i/Channels, out[i], out[i+1])
		}
		pairs++
		if out[i] != 0 {
			span++
		}
	}
	if pairs != want/Channels {
		t.Fatalf("queued region is %d stereo pairs, want %d", pairs, want/Channels)
	}
	if span < pairs-8 {
		t.Fatalf("only %d of %d stereo pairs carry audio — the frame is not filling the period", span, pairs)
	}
}

// TestPlayMonoKeepsItsOwnResampler pins why PlayMono does not borrow p.voice:
// assistant replies reset the voice resampler (Drain) and hold it across the
// whole conversion, either of which would corrupt a continuous call stream.
func TestPlayMonoKeepsItsOwnResampler(t *testing.T) {
	p := New()
	if p.monoUp == nil || p.voice == nil {
		t.Fatalf("New() left a resampler nil (monoUp=%v voice=%v)", p.monoUp, p.voice)
	}
	if reflect.ValueOf(p.monoUp).Pointer() == reflect.ValueOf(p.voice).Pointer() {
		t.Fatal("PlayMono shares the voice resampler; Drain/PlayVoice would corrupt call audio")
	}
}
