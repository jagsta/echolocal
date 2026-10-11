package defaults

import (
	"testing"

	"github.com/ygelfand/echolocal/internal/board"
)

func TestBiscuitIsWhatItAlwaysWas(t *testing.T) {
	got := For(board.Biscuit)

	want := Set{
		MicGain:     20,
		Sensitivity: 8,
		RingTrouble: "Alert",
		RingMuted:   "",
		MinCores:    2,

		VisualizerLift: 10,

		IntercomMaxMinutes: 30,
	}
	if got != want {
		t.Errorf("biscuit's defaults are %+v, want %+v", got, want)
	}
}

func TestTheShowsStartTheVisualsUnlifted(t *testing.T) {
	for _, b := range []board.Board{board.Checkers, board.Cronos} {
		if got := For(b).VisualizerLift; got != 0 {
			t.Errorf("%s lifts the visuals %d dB, want 0", b.Codename, got)
		}
	}
}

func TestAnUnmeasuredBoardBorrowsBiscuits(t *testing.T) {
	if got := For(board.Donut); got != For(board.Biscuit) {
		t.Errorf("donut starts from %+v, want biscuit's %+v", got, For(board.Biscuit))
	}
}

func TestCurrentIsBiscuitUntilToldOtherwise(t *testing.T) {
	if got := Current(); got != For(board.Biscuit) {
		t.Errorf("Current() = %+v before Use, want biscuit's %+v", got, For(board.Biscuit))
	}
}
