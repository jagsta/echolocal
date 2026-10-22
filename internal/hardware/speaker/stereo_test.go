package speaker

import (
	"testing"

	"github.com/ygelfand/echolocal/internal/config"
)

func TestOneDriverHearsBothChannels(t *testing.T) {
	p := newTestPlayer()
	p.out = OutputSpeaker
	p.bus.Push(config.StreamMedia, []int16{100, 300})

	got := played(t, p)
	if got[0] != 200 || got[1] != 200 {
		t.Errorf("a mono board played %d %d, want 200 on both", got[0], got[1])
	}
}

func TestTwoDriversKeepLeftAndRight(t *testing.T) {
	p := newTestPlayer()
	p.out, p.stereo = OutputSpeaker, true
	p.bus.Push(config.StreamMedia, []int16{100, 300})

	got := played(t, p)
	if got[0] != 100 || got[1] != 300 {
		t.Errorf("a stereo board played %d %d, want 100 and 300", got[0], got[1])
	}
}
