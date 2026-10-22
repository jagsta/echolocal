package media

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/libcountertop/pkg/audio/noise"
	sharedvolume "github.com/ygelfand/libcountertop/pkg/audio/volume"
	"github.com/ygelfand/libcountertop/pkg/hook"
	"github.com/ygelfand/libcountertop/pkg/media/mediaplayer"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/feedback"
	"github.com/ygelfand/echolocal/internal/hardware/buttons"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

func init() {
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(25))
}

// Android's STREAM_MUSIC and the vendor volume curves run 0..30.
const VolumeSteps = speaker.VolumeSteps

const volumeFlash = 2 * time.Second

type Player struct {
	*mediaplayer.Player[*speaker.Player]

	jack *esphome.BinarySensor

	resampling *esphome.Select
	duck       *sharedvolume.Ducking

	asp *esphome.Switch

	// The native API has no entity that holds more than one value.
	layers []*esphome.Select

	Volume hook.Hook[int]
	Levels *sharedvolume.Volume

	screen atomic.Pointer[Screen]

	step atomic.Int64
}

var (
	once   sync.Once
	shared *Player
)

func Get() *Player {
	once.Do(func() { shared = build() })
	return shared
}

func build() *Player {
	p := &Player{
		jack: &esphome.BinarySensor{
			Base: esphome.Base{
				ObjectID: "headphones",
				Name:     "Headphones",
				Icon:     "mdi:headphones",
				Category: esphome.CategoryDiagnostic,
			},
			DeviceClass: "plug",
		},
		resampling: &esphome.Select{
			Base: esphome.Base{
				ObjectID: "voice_resampling",
				Name:     "Voice resampling",
				Icon:     "mdi:sine-wave",
				Category: esphome.CategoryConfig,
			},
		},
		duck: sharedvolume.NewDucking(config.MediaSection, component.DevicePlayback),
		asp: &esphome.Switch{
			Base: esphome.Base{
				ObjectID: "speaker_eq",
				Name:     "Speaker EQ",
				Icon:     "mdi:equalizer",
				Category: esphome.CategoryConfig,
			},
		},
	}
	p.layers = noiseLayers()
	component.Settings.Add(p.duck)
	p.Levels = sharedvolume.NewVolume(sharedvolume.Options{
		Mixer: sharedvolume.MixerOptions{
			Output:  speaker.Get(),
			Streams: config.Streams(),
			Save: func(s config.Stream, level int) error {
				return config.Set().Volume().Level(s, level)
			},
			Device: component.DevicePlayback,
		},
		Main:      sharedvolume.Steps(p, VolumeSteps),
		MainSteps: VolumeSteps,
	})
	p.Levels.Watch(config.StreamMedia, func(int) { p.Changed() })

	bases := []*esphome.Base{
		&p.resampling.Base,
		&p.jack.Base,
		&p.asp.Base,
	}
	for _, sel := range p.layers {
		bases = append(bases, &sel.Base)
	}
	for _, b := range bases {
		b.DeviceID = component.DevicePlayback
	}

	p.Player = mediaplayer.New(mediaplayer.Options[*speaker.Player]{
		Speaker:  speaker.Get(),
		Sound:    speaker.Sound(),
		Rate:     speaker.Rate,
		Channels: speaker.Channels,
		DuckDB:   func() float64 { return config.Get().Media.DuckDB },
		Volume:   mediaVolume{p},
		Changed:  p.refresh,
		Device:   component.DevicePlayback,
	})
	component.Bind(p.resampling, speaker.Resamplings(), speaker.Get().SetResampling,
		config.Set().Speaker().Resampling)

	p.asp.OnCommand = func(want bool) {
		settled := speaker.Get().SetASP(want)
		p.asp.Set(settled)
		if err := config.Set().Speaker().ASP(settled); err != nil {
			slog.Error("saving a setting failed", "setting", p.asp.ObjectID, "err", err)
		}
		slog.Info("setting changed", "setting", p.asp.ObjectID, "using", settled, "asked", want)
	}

	for _, sel := range p.layers {
		sel.OnCommand = func(chosen string) {
			if chosen != noiseOff && !noise.Has(chosen) {
				slog.Warn("unknown option", "setting", sel.ObjectID, "value", chosen)
				return
			}

			sel.Set(chosen)
			p.sound()
		}
	}

	buttons.Get().Events.Listen(func(e buttons.Event) {
		if e.Kind == buttons.Hold {
			return
		}
		switch e.Name {
		case buttons.VolumeUp:
			p.turn(1)
		case buttons.VolumeDown:
			p.turn(-1)
		}
	})

	spk := speaker.Get()
	spk.OnOutput.Listen(func(out speaker.Output) { p.jack.Set(out == speaker.OutputHeadphone) })
	p.jack.Set(spk.Output() == speaker.OutputHeadphone)

	for _, sel := range p.layers {
		sel.Set(noiseOff)
	}
	return p
}

func (p *Player) Name() string { return "media player" }

func (p *Player) WatchVolume(changed func(int)) func() { return p.Volume.Listen(changed) }

func (p *Player) Entities() []esphome.Entity {
	out := append([]esphome.Entity{p.Entity(), p.jack, p.resampling}, p.Track()...)
	out = append(out, p.Levels.Entities()...)
	out = append(out, p.duck.Entities()...)
	out = append(out, p.asp)
	for _, sel := range p.layers {
		out = append(out, sel)
	}
	return out
}

func (p *Player) Restore(c config.Config) {
	p.apply(c.Speaker.Volume, false)
	slog.Info("restored", "what", "volume", "step", c.Speaker.Volume, "of", VolumeSteps)
	p.Levels.Restore(c.Volume.Level)
	p.Changed()

	component.Restore(p.resampling, c.Speaker.Resampling, speaker.Get().SetResampling)

	p.duck.PublishFrom(c.Media)

	settled := speaker.Get().SetASP(c.Speaker.ASP)
	p.asp.Set(settled)
	slog.Info("restored", "what", p.asp.ObjectID, "using", settled, "asked", c.Speaker.ASP)
}

const noiseOff = "None"

func noiseLayers() []*esphome.Select {
	const slots = 2

	out := make([]*esphome.Select, 0, slots)
	for i := 1; i <= slots; i++ {
		out = append(out, &esphome.Select{
			Base: esphome.Base{
				ObjectID: fmt.Sprintf("noise_layer_%d", i),
				Name:     fmt.Sprintf("White noise layer %d", i),
				Icon:     "mdi:blur",
			},
			Options: append([]string{noiseOff}, noise.Names()...),
		})
	}
	return out
}

func (p *Player) sound() {
	var sounds []string
	for _, sel := range p.layers {
		if chosen := sel.Get(); chosen != noiseOff && chosen != "" {
			sounds = append(sounds, chosen)
		}
	}

	if len(sounds) == 0 {
		p.Stream().Stop()
		return
	}
	p.Stream().PlayNoise(sounds...)
}

type mediaVolume struct{ p *Player }

func (v mediaVolume) SetVolume(fraction float32) {
	v.p.Levels.Set(config.StreamMedia, int(math.Round(float64(fraction)*100)))
}

func (v mediaVolume) StepVolume(delta int) {
	levels := v.p.Levels
	levels.Set(config.StreamMedia, levels.Level(config.StreamMedia)+delta*sharedvolume.Step)
}

func (v mediaVolume) Mute(muted bool) { v.p.Levels.Mute(config.StreamMedia, muted) }

func (p *Player) SetIdle(v schema.Delay) {
	if err := config.Set().Idle().Media(v); err != nil {
		slog.Warn("saving the media idle timeout", "err", err)
	}
}

func (p *Player) refresh() {
	p.Entity().SetVolume(float32(p.Levels.Level(config.StreamMedia)) / 100)
	p.Entity().SetMuted(p.Levels.Muted(config.StreamMedia))
	if len(p.Stream().Noise()) == 0 {
		for _, sel := range p.layers {
			sel.Set(noiseOff)
		}
	}
}

func (p *Player) Set(step int) {
	applied := p.apply(step, true)
	if err := config.Set().Speaker().Volume(applied); err != nil {
		slog.Error("saving volume failed", "err", err)
	}
}

func (p *Player) apply(step int, tell bool) int {
	step = max(0, min(step, VolumeSteps))
	p.step.Store(int64(step))

	speaker.Get().SetVolume(step)
	if !tell {
		return step
	}

	p.show(float64(step) / VolumeSteps)
	p.Volume.Emit(step)
	slog.Info("volume", "step", step, "of", VolumeSteps)
	return step
}

func (p *Player) Step() int { return int(p.step.Load()) }

func (p *Player) Settings() *sharedvolume.Ducking { return p.duck }

type Screen interface {
	Selected() (config.Stream, bool)
	Top() any
}

func (p *Player) SetScreen(s Screen) { p.screen.Store(&s) }

func (p *Player) turn(delta int) {
	aim := sharedvolume.Aim{
		Mode:  config.Get().Media.Buttons,
		Heard: func(s config.Stream) bool { return speaker.Get().Heard(s, sharedvolume.Recent) },
	}
	if s := p.screen.Load(); s != nil {
		aim.Picked, aim.Screen = (*s).Selected, (*s).Top
	}
	s := sharedvolume.Target(aim)
	if s == config.StreamMain {
		p.Adjust(delta)
		return
	}
	p.Levels.Set(s, p.Levels.Level(s)+delta*sharedvolume.Step)
	feedback.Get().Preview(s)
	p.show(float64(p.Levels.Level(s)) / 100)
}

func (p *Player) Adjust(delta int) {
	p.Set(p.Step() + delta)
	feedback.Get().Preview(config.StreamMain)
}

func (p *Player) show(fraction float64) {
	leds := led.Get()
	if !leds.Present() {
		return
	}

	frame := led.Volume(fraction)
	leds.Claim(led.PriorityNotice).PaintFor(frame, volumeFlash)
}

func (p *Player) Start(ctx context.Context) error { p.Player.Start(ctx); return nil }
func (p *Player) Run(ctx context.Context) error   { <-ctx.Done(); return nil }
