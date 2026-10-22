package buttons

import (
	"testing"
	"time"

	"github.com/ygelfand/libcountertop/pkg/input"
)

func press(c *Controller, down map[uint16]*held, code uint16, at, until uint64) {
	c.key(input.Event{Type: input.EvKey, Code: code, Value: 1, Sec: at / 1e6, Usec: at % 1e6}, down)
	c.key(
		input.Event{Type: input.EvKey, Code: code, Value: 0, Sec: until / 1e6, Usec: until % 1e6},
		down,
	)
}

func TestAnInstantMutePressIsPower(t *testing.T) {
	c := &Controller{}
	var got []Event
	c.Events.Listen(func(e Event) { got = append(got, e) })
	down := map[uint16]*held{}

	press(c, down, 113, 2_559_640_067, 2_559_830_078)
	press(c, down, 113, 2_560_950_087, 2_560_950_127)

	want := []Event{{Name: Mute, Kind: Tap}, {Name: Power, Kind: Tap}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d is %v, want %v", i, got[i], want[i])
		}
	}
}

func listening(c *Controller) <-chan Event {
	got := make(chan Event, 8)
	c.Events.Listen(func(e Event) { got <- e })
	return got
}

func settled(t *testing.T, got <-chan Event) []Event {
	t.Helper()
	time.Sleep(MultiTap + 200*time.Millisecond)

	var out []Event
	for {
		select {
		case e := <-got:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestActionTapsAreCounted(t *testing.T) {
	for taps, want := range map[int]Kind{1: Tap, 2: DoubleTap, 3: TripleTap, 4: TripleTap} {
		c := &Controller{}
		got := listening(c)
		down := map[uint16]*held{}

		for i := range taps {
			at := uint64(i) * 100_000
			press(c, down, 138, at, at+50_000)
		}

		events := settled(t, got)
		if len(events) != 1 || events[0] != (Event{Name: Action, Kind: want}) {
			t.Errorf("%d taps gave %v, want one %s", taps, events, want)
		}
	}
}

func TestOtherButtonsAreNotCounted(t *testing.T) {
	c := &Controller{}
	got := listening(c)
	down := map[uint16]*held{}

	press(c, down, 113, 0, 50_000)
	press(c, down, 113, 100_000, 150_000)

	events := settled(t, got)
	want := Event{Name: Mute, Kind: Tap}
	if len(events) != 2 || events[0] != want || events[1] != want {
		t.Errorf("two mute taps gave %v, want two taps", events)
	}
}

func TestAHoldClearsTheTapCount(t *testing.T) {
	c := &Controller{}
	got := listening(c)
	down := map[uint16]*held{}

	press(c, down, 138, 0, 50_000)
	c.key(input.Event{Type: input.EvKey, Code: 138, Value: 1}, down)
	time.Sleep(LongPress + 100*time.Millisecond)
	c.key(input.Event{Type: input.EvKey, Code: 138, Value: 0, Sec: 1}, down)

	events := settled(t, got)
	if len(events) != 1 || events[0] != (Event{Name: Action, Kind: Hold}) {
		t.Errorf("a tap then a hold gave %v, want one hold", events)
	}
}
