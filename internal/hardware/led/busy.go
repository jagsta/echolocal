package led

import (
	"log/slog"
	"slices"
	"sync"
	"time"
)

type Work int

const (
	WorkWakeWord Work = iota

	WorkElsewhere

	WorkUpdate
)

var UpdateColor = Color{R: 0x00, G: 0xB0, B: 0xC0}

const busyLimit = time.Minute

func (w Work) appearance() (string, Color) {
	switch w {
	case WorkWakeWord:
		// Stock Alexa semantics (LED card t_05fc05ad): the listen sweep is
		// BLUE, not the old amber chase. A palette-driven effect ignores the
		// base colour, so the fixed Alexa blue is honoured regardless of the
		// user's ring-light setting.
		return EffectBlueChase, Color{}
	case WorkElsewhere:
		return EffectChase, Color{R: 0x80, G: 0x40, B: 0xC0}
	case WorkUpdate:
		return EffectChase, UpdateColor
	}
	return EffectChase, Color{R: 0xFF, G: 0xFF, B: 0xFF}
}

func (w Work) String() string {
	switch w {
	case WorkWakeWord:
		return "wake word"
	case WorkElsewhere:
		return "answered elsewhere"
	case WorkUpdate:
		return "update"
	}
	return "work"
}

type Busy struct {
	claim *Claim

	mu    sync.Mutex
	tasks []*Task
}

type Task struct {
	busy  *Busy
	work  Work
	timer *time.Timer
}

func (b *Busy) Start(w Work) *Task {
	b.mu.Lock()
	defer b.mu.Unlock()

	t := &Task{busy: b, work: w}
	t.timer = time.AfterFunc(busyLimit, func() {
		slog.Warn("giving up a busy indication nothing finished", "work", w, "after", busyLimit)
		t.Done()
	})

	b.tasks = append(b.tasks, t)
	b.show()
	return t
}

func (b *Busy) Flash(w Work, d time.Duration) {
	t := b.Start(w)
	time.AfterFunc(d, t.Done)
}

func (t *Task) Done() {
	if t == nil {
		return
	}

	t.busy.mu.Lock()
	defer t.busy.mu.Unlock()

	i := slices.Index(t.busy.tasks, t)
	if i < 0 {
		return
	}
	t.timer.Stop()
	t.busy.tasks = slices.Delete(t.busy.tasks, i, i+1)
	t.busy.show()
}

func (t *Task) Refresh() {
	if t == nil {
		return
	}

	t.busy.mu.Lock()
	defer t.busy.mu.Unlock()

	if slices.Contains(t.busy.tasks, t) {
		t.timer.Reset(busyLimit)
	}
}

func (b *Busy) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.tasks) > 0
}

func (b *Busy) show() {
	if len(b.tasks) == 0 {
		b.claim.Clear()
		return
	}
	b.claim.Play(b.tasks[len(b.tasks)-1].work.appearance())
}
