package visuals

import (
	"sync"

	"github.com/ygelfand/libcountertop/pkg/display/visualinput"
	"github.com/ygelfand/libcountertop/pkg/display/visualview"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/mute"
	"github.com/ygelfand/echolocal/internal/feature/shell"
	"github.com/ygelfand/echolocal/internal/hardware/display"
	"github.com/ygelfand/echolocal/internal/hardware/gpu"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

const LiftMax = visualinput.LiftMax

type Visuals struct{ *visualinput.Visuals }

func init() {
	component.Register(
		sharedcomponent.Device,
		Get,
		sharedcomponent.Order(60),
		component.Needs(board.Panel),
	)
}

var get = sync.OnceValue(func() *Visuals {
	v := &Visuals{visualinput.New(visualinput.Options{
		Settings:        config.VisualSection,
		MicRate:         16000,
		MicChannels:     1,
		SpeakerRate:     speaker.Rate,
		SpeakerChannels: speaker.Channels,
		Lift:            config.LiftSection,
		DeviceID:        component.DeviceScreen,
		Listen:          func() (<-chan []int16, func()) { return mic.Get().ListenUnleveled("visuals") },
		Muted:           func() bool { m, _ := mute.Get().Muted(); return m },
		Tap:             func(t visualinput.Tap) { speaker.Get().SetTap(t) },
	})}
	component.Settings.Add(v.Settings())
	return v
})

func Get() *Visuals { return get() }

func (v *Visuals) Restore(c config.Config) { v.Visuals.Restore(c.Visual) }

func (v *Visuals) View() *visualview.View { return Show(v) }

func Show(source visualview.Source) *visualview.View {
	return visualview.New(source, visualview.Dependencies{
		GPU:      gpu.Get(),
		Display:  display.Get(),
		Shell:    shell.Get(),
		Settings: config.VisualSection,
	})
}
