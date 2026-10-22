package buttons

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ygelfand/libcountertop/pkg/hook"
	"github.com/ygelfand/libcountertop/pkg/input"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/runtime/service"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
)

func init() {
	component.Register(sharedcomponent.Hardware, Get, sharedcomponent.Order(10),
		sharedcomponent.Supervise(service.Restart(time.Second, 30*time.Second)))
}

type Name = board.Key

const (
	Mute       = board.Mute
	VolumeDown = board.VolumeDown
	VolumeUp   = board.VolumeUp
	Action     = board.Action
	Power      = board.Power
)

// cronos's driver sends KEY_POWER down and up 40µs apart about a second after each cut; real presses measured 90-230ms.
const instant = time.Millisecond

var codes = map[uint16]Name{
	113: Mute,
	114: VolumeDown,
	115: VolumeUp,
	138: Action,
}

func keyFor(code uint16) (Name, bool) {
	if n, ok := component.Board().Keys[code]; ok {
		return n, true
	}
	n, ok := codes[code]
	return n, ok
}

const (
	LongPress      = 700 * time.Millisecond
	RepeatInterval = 200 * time.Millisecond
	MultiTap       = 300 * time.Millisecond
)

type Kind string

const (
	Tap Kind = "tap"

	Hold Kind = "hold"

	Repeat Kind = "repeat"

	DoubleTap Kind = "double"

	TripleTap Kind = "triple"
)

type Event struct {
	Name Name
	Kind Kind
}

func (e Event) String() string { return string(e.Name) + " " + string(e.Kind) }

func repeats(n Name) bool { return n == VolumeUp || n == VolumeDown }

func counted(n Name) bool { return n == Action }

func tapKind(taps int) Kind {
	switch taps {
	case 1:
		return Tap
	case 2:
		return DoubleTap
	default:
		return TripleTap
	}
}

type Controller struct {
	Events hook.Hook[Event]

	Shutter hook.Hook[bool]

	mu      sync.Mutex
	takers  []func(Event) bool
	devices []*input.Device
	shutter bool
	covered bool

	cut, cutKnown bool

	taps    int
	tapping *time.Timer
}

var (
	once   sync.Once
	shared *Controller
)

func Get() *Controller {
	once.Do(func() { shared = &Controller{} })
	return shared
}

func (c *Controller) Name() string { return "buttons" }

func (c *Controller) Start(context.Context) error {
	devices, err := input.List()
	if err != nil {
		return fmt.Errorf("buttons: listing input devices: %w", err)
	}
	if len(devices) == 0 {
		return fmt.Errorf("buttons: no input devices")
	}

	shutter := component.Board().Has(board.Shutter)

	c.mu.Lock()
	c.devices = devices
	c.shutter = shutter
	c.mu.Unlock()

	if shutter {
		c.findShutter(devices)
	}
	c.findMute(devices)

	slog.Debug("buttons ready", "devices", len(devices))
	return nil
}

func (c *Controller) findShutter(devices []*input.Device) {
	for _, d := range devices {
		if !d.HasSwitch(input.SwCameraLensCover) {
			continue
		}
		covered, err := d.Switch(input.SwCameraLensCover)
		if err != nil {
			slog.Warn("reading the camera cover", "device", d.Path, "err", err)
			return
		}
		c.setCovered(covered)
		return
	}
	slog.Warn("no camera cover on a board that should have one")
}

func (c *Controller) findMute(devices []*input.Device) {
	for _, d := range devices {
		if !d.HasSwitch(input.SwMuteDevice) {
			continue
		}
		cut, err := d.Switch(input.SwMuteDevice)
		if err != nil {
			slog.Warn("reading the microphone cut", "device", d.Path, "err", err)
			return
		}
		c.setCut(cut)
		return
	}
}

func (c *Controller) setCut(cut bool) {
	c.mu.Lock()
	c.cut, c.cutKnown = cut, true
	c.mu.Unlock()
}

func (c *Controller) MuteSwitch() (cut, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cut, c.cutKnown
}

func (c *Controller) setCovered(covered bool) {
	c.mu.Lock()
	changed := c.covered != covered
	c.covered = covered
	c.mu.Unlock()

	if changed {
		slog.Info("camera cover", "covered", covered)
		c.Shutter.Emit(covered)
	}
}

func (c *Controller) Covered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.covered
}

func (c *Controller) Close() error {
	c.mu.Lock()
	devices := c.devices
	c.devices = nil
	c.mu.Unlock()

	for _, d := range devices {
		_ = d.Close()
	}
	return nil
}

func (c *Controller) Run(ctx context.Context) error {
	c.mu.Lock()
	devices := c.devices
	c.mu.Unlock()

	failed := make(chan error, len(devices))
	for _, d := range devices {
		go func(d *input.Device) { failed <- c.watch(ctx, d) }(d)
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		return err
	}
}

// This keypad emits no autorepeat, only press and release.
type held struct {
	at     time.Duration
	mu     sync.Mutex
	long   bool
	timer  *time.Timer
	ticker *time.Ticker
	done   chan struct{}
}

func (h *held) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.timer != nil {
		h.timer.Stop()
	}
	if h.done != nil {
		close(h.done)
		h.done = nil
	}
	if h.ticker != nil {
		h.ticker.Stop()
	}
}

func (c *Controller) Take(f func(Event) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.takers = append(c.takers, f)
}

func (c *Controller) Press(e Event) {
	c.mu.Lock()
	takers := c.takers
	c.mu.Unlock()
	for _, take := range takers {
		if take(e) {
			return
		}
	}
	c.Events.Emit(e)
}

func (c *Controller) emit(name Name, kind Kind) { c.Press(Event{Name: name, Kind: kind}) }

func (c *Controller) watch(ctx context.Context, d *input.Device) error {
	down := map[uint16]*held{}
	defer func() {
		for _, h := range down {
			h.stop()
		}
	}()

	for {
		e, err := d.Read()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("buttons: reading %s: %w", d.Path, err)
		}
		switch e.Type {
		case input.EvKey:
			c.key(e, down)
		case input.EvSw:
			if c.shutter && e.Code == input.SwCameraLensCover {
				c.setCovered(e.Value != 0)
			}
			if e.Code == input.SwMuteDevice {
				c.setCut(e.Value != 0)
			}
		}
	}
}

func (c *Controller) key(e input.Event, down map[uint16]*held) {
	name, ok := keyFor(e.Code)
	if !ok {
		return
	}

	switch e.Value {
	case 1:
		h := c.pressed(name)
		h.at = e.At()
		down[e.Code] = h
	case 0:
		h, ok := down[e.Code]
		if !ok {
			return
		}
		delete(down, e.Code)
		if name == Mute && e.At()-h.at < instant {
			name = Power
		}
		c.released(name, h)
	}
}

func (c *Controller) pressed(name Name) *held {
	h := &held{}

	if counted(name) {
		c.mu.Lock()
		if c.tapping != nil {
			c.tapping.Stop()
		}
		c.mu.Unlock()
	}

	if repeats(name) {
		c.emit(name, Tap)

		h.done = make(chan struct{})
		h.ticker = time.NewTicker(RepeatInterval)
		go func(done <-chan struct{}, t *time.Ticker) {
			for {
				select {
				case <-done:
					return
				case <-t.C:
					c.emit(name, Repeat)
				}
			}
		}(h.done, h.ticker)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.timer = time.AfterFunc(LongPress, func() {
		h.mu.Lock()
		h.long = true
		h.mu.Unlock()

		if !repeats(name) {
			h.stop()
		}
		if counted(name) {
			c.mu.Lock()
			c.taps = 0
			c.mu.Unlock()
		}
		c.emit(name, Hold)
	})
	return h
}

func (c *Controller) released(name Name, h *held) {
	h.stop()

	h.mu.Lock()
	long := h.long
	h.mu.Unlock()

	if long || repeats(name) {
		return
	}
	if counted(name) {
		c.count(name)
		return
	}
	c.emit(name, Tap)
}

func (c *Controller) count(name Name) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.taps++
	c.tapping = time.AfterFunc(MultiTap, func() { c.settle(name) })
}

func (c *Controller) settle(name Name) {
	c.mu.Lock()
	taps := c.taps
	c.taps, c.tapping = 0, nil
	c.mu.Unlock()

	if taps > 0 {
		c.emit(name, tapKind(taps))
	}
}
