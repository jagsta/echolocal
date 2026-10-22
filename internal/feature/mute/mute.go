package mute

import (
	"log/slog"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	sharedtone "github.com/ygelfand/libcountertop/pkg/audio/tone"
	"github.com/ygelfand/libcountertop/pkg/hook"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	setting "github.com/ygelfand/libcountertop/pkg/settings"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/feedback"
	"github.com/ygelfand/echolocal/internal/feature/tones"
	"github.com/ygelfand/echolocal/internal/hardware/buttons"
	"github.com/ygelfand/echolocal/internal/hardware/indicate"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/echolocal/internal/hardware/privacy"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

func init() {
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(30))
}

// The mute LED has two levels.
const (
	dim    = "Dim"
	bright = "Bright"
)

var mutedColor = led.Color{R: 0xC0, G: 0x00, B: 0x00}

type Mute struct {
	Changed hook.Hook[bool]

	sw         *esphome.Switch
	brightness *esphome.Select
	sound      *setting.Controls[config.Microphone]
	line       privacy.Mute
	led        privacy.LED

	ring  *esphome.Select
	claim indicate.Claim
}

var (
	once   sync.Once
	shared *Mute
)

func Get() *Mute {
	once.Do(func() { shared = build() })
	return shared
}

func build() *Mute {
	m := &Mute{
		sw: &esphome.Switch{
			Base: esphome.Base{
				ObjectID: "mic_mute",
				DeviceID: component.DeviceMicrophone,
				Name:     "Microphone mute",
				Icon:     "mdi:microphone-off",
			},
		},
		claim: indicate.Get().Claim(indicate.PriorityMute),

		ring: &esphome.Select{
			Base: esphome.Base{
				ObjectID: "ring_muted",
				DeviceID: component.DeviceRing,
				Name:     "Ring while muted",
				Icon:     "mdi:microphone-off",
				Category: esphome.CategoryConfig,
			},
		},
	}
	m.sw.OnCommand = m.Set
	m.sound = &setting.Controls[config.Microphone]{
		Table: sharedtone.MuteTable(
			func(c *config.Microphone) *schema.Chime { return &c.MuteSound },
			tones.Get().Choices,
		),
		Device: component.DeviceMicrophone,
		Read:   config.MicrophoneSection.Read,
		Save: func(s setting.Setting[config.Microphone], v string) error {
			return setting.Store(config.MicrophoneSection, s, v)
		},
	}
	component.Settings.Add(m.sound)
	tones.Get().Changed.Listen(func(struct{}) { m.sound.Refresh() })

	component.BindEffect(m.ring, led.EffectNames(), m.show, config.Set().Ring().Muted)

	m.brightness = &esphome.Select{
		Base: esphome.Base{
			ObjectID: "mute_led_brightness",
			DeviceID: component.DeviceMicrophone,
			Name:     "Mute LED brightness",
			Icon:     "mdi:brightness-6",
			Category: esphome.CategoryConfig,
		},
		Options: []string{dim, bright},
	}
	m.brightness.OnCommand = m.setBrightness

	var err error
	if m.line, err = privacy.Microphone(); err != nil {
		slog.Error("mute unavailable", "err", err)
	}
	if component.Board().Has(board.MuteLight) {
		if m.led, err = privacy.Light(); err != nil {
			slog.Error("mute LED unavailable", "err", err)
		}
	}

	buttons.Get().Events.Listen(m.pressed)
	return m
}

func (m *Mute) Name() string { return "microphone mute" }

func (m *Mute) Entities() []esphome.Entity {
	ents := append([]esphome.Entity{m.sw, m.brightness}, m.sound.Entities()...)
	if component.Board().Has(board.Ring) {
		ents = append(ents, m.ring)
	}
	return ents
}

func (m *Mute) Muted() (bool, error) {
	if m.line == nil {
		return false, nil
	}
	return m.line.Get()
}

// The mute line does not survive a reboot.
func (m *Mute) Restore(c config.Config) {
	m.sound.Publish()

	if m.line == nil {
		return
	}

	component.RestoreEffect(m.ring, c.Ring.Muted, nil, config.Set().Ring().Muted)

	want := c.Microphone.Muted
	if err := m.line.Set(want); err != nil {
		slog.Error("setting mute failed", "muted", want, "err", err)
	}
	m.settled(false)
	slog.Info("restored", "what", "microphone mute", "muted", m.sw.Get(), "asked", want)

	m.applyBrightness(c.Microphone.LEDBright)
	slog.Info("restored", "what", m.brightness.ObjectID, "using", label(c.Microphone.LEDBright))
}

func (m *Mute) Set(muted bool) {
	if m.line == nil {
		return
	}
	if err := m.line.Set(muted); err != nil {
		slog.Error("setting mute failed", "muted", muted, "err", err)
		m.settled(false)
		return
	}
	m.settled(true)
}

func (m *Mute) Toggle() {
	if m.line == nil {
		return
	}
	if !m.line.HardwareToggles() {
		if _, err := m.line.Toggle(); err != nil {
			slog.Error("toggling mute failed", "err", err)
			return
		}
	}
	m.settled(true)
}

func (m *Mute) pressed(e buttons.Event) {
	if e.Name != buttons.Mute {
		return
	}
	switch e.Kind {
	case buttons.Tap:
		m.Toggle()
	case buttons.Hold:
		feedback.Get().Hold()
	}
}

func (m *Mute) settled(asked bool) {
	if asked {
		m.await(m.sw.Get())
	}

	muted, err := m.line.Get()
	if err != nil {
		slog.Error("reading mute state failed", "err", err)
		return
	}

	m.sw.Set(muted)
	m.show(component.ChosenEffect(m.ring))
	m.Changed.Emit(muted)

	if !asked {
		return
	}

	if err := config.Set().Microphone().Muted(muted); err != nil {
		slog.Error("saving mute state failed", "err", err)
	}
	slog.Info("microphone mute", "muted", muted)

	snd := sharedtone.MuteSound(tones.Get(), config.Get().Microphone.MuteSound, muted)
	if !snd.Empty() {
		speaker.Sound().Sound(config.StreamFeedback, sharedtone.Level, snd)
	}
}

const pollInterval = 25 * time.Millisecond

func (m *Mute) await(from bool) {
	for waited := time.Duration(0); waited < m.line.Lag(); waited += pollInterval {
		if is, err := m.line.Get(); err != nil || is != from {
			return
		}
		time.Sleep(pollInterval)
	}
}

func (m *Mute) show(name string) {
	if name == "" || !m.sw.Get() {
		m.claim.Clear()
		return
	}
	m.claim.Play(name, mutedColor)
}

func (m *Mute) setBrightness(v string) {
	on := v == bright
	m.applyBrightness(on)
	if err := config.Set().Microphone().LEDBright(on); err != nil {
		slog.Error("saving mute LED brightness failed", "err", err)
	}
}

func (m *Mute) applyBrightness(on bool) {
	if m.led == nil {
		return
	}
	if err := m.led.SetBright(on); err != nil {
		slog.Error("setting mute LED brightness failed", "bright", on, "err", err)
		return
	}
	if now, err := m.led.Bright(); err == nil {
		m.brightness.Set(label(now))
	}
}

func label(on bool) string {
	if on {
		return bright
	}
	return dim
}
