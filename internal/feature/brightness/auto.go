package brightness

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/libcountertop/pkg/display/brightness"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/runtime/safe"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/diag"
	"github.com/ygelfand/echolocal/internal/feature/screen"
	"github.com/ygelfand/echolocal/internal/hardware/alsps"
	"github.com/ygelfand/echolocal/internal/hardware/display"
)

func init() {
	component.Register(
		sharedcomponent.Device,
		Get,
		sharedcomponent.Order(41),
		component.Needs(board.Panel),
	)
	diag.Ambient = alsps.Ambient
}

const (
	tick = 250 * time.Millisecond

	// The driver reports whole lux.
	leastLux = 1
)

type Auto struct {
	ambient *esphome.Sensor

	auto *brightness.Auto

	mu     sync.Mutex
	sensor *alsps.Sensor
}

var (
	once   sync.Once
	shared *Auto
)

func Get() *Auto {
	once.Do(func() { shared = build() })
	return shared
}

func build() *Auto {
	return &Auto{
		auto: brightness.NewAuto(leastLux),
		ambient: &esphome.Sensor{
			Base: esphome.Base{
				ObjectID: "ambient_light", Name: "Ambient light", Icon: "mdi:brightness-5",
				DeviceID: component.DeviceScreen,
			},
			Unit: "lx", DeviceClass: "illuminance", StateClass: esphome.StateClassMeasurement,
		},
	}
}

func (a *Auto) Name() string { return "brightness" }

func (a *Auto) Entities() []esphome.Entity {
	return []esphome.Entity{a.ambient}
}

func (a *Auto) Start(context.Context) error {
	s, err := alsps.Open()
	if err != nil {
		slog.Warn("no ambient light sensor, brightness is manual only", "err", err)
		return nil
	}
	a.mu.Lock()
	a.sensor = s
	a.mu.Unlock()
	return nil
}

func (a *Auto) Run(ctx context.Context) error {
	a.mu.Lock()
	s := a.sensor
	a.mu.Unlock()
	if s == nil {
		<-ctx.Done()
		return nil
	}

	failed := make(chan error, 1)
	safe.Go("ambient light", func() { failed <- s.Run(ctx) })

	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failed:
			return err
		case <-t.C:
		}
		if lux, ok := s.Lux(); ok {
			a.follow(lux)
		}
	}
}

func (a *Auto) Close() error {
	a.mu.Lock()
	s := a.sensor
	a.sensor = nil
	a.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Close()
}

func (a *Auto) Ambient() (float64, bool) { return a.auto.Ambient() }

func (a *Auto) Night(n schema.Night) { a.auto.Night(n) }

func (a *Auto) Rising(share func() (float64, bool)) { a.auto.Rising(share) }

func (a *Auto) follow(lux float64) {
	step := a.auto.Follow(lux, config.Get().Screen)
	if step.Report {
		a.ambient.Set(float32(math.Round(step.Ambient*10) / 10))
	}
	if step.Apply {
		a.apply(step.Level)
	}
}

func (a *Auto) apply(level int) {
	if err := display.Get().Brightness(level); err != nil {
		slog.Warn("setting the backlight failed", "err", err)
		return
	}
	screen.Get().Lit(level)
}
