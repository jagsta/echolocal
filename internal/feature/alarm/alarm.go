package alarm

import (
	"sync"
	"time"

	"github.com/ygelfand/libcountertop/pkg/assistant/alarm"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/runtime/remember"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/feature/alerts"
	featureremember "github.com/ygelfand/echolocal/internal/feature/remember"
	"github.com/ygelfand/echolocal/internal/feature/timesync"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

func init() {
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(32))
}

var (
	ringingColor = led.Color{R: 0xFF, G: 0x40, B: 0x00}
	dawnColor    = led.Color{R: 0xFF, G: 0xC8, B: 0x8C}
)

var get = sync.OnceValue(func() *alarm.Alarms {
	ringing := led.Get().Claim(led.PriorityAlarm)
	dawning := led.Get().Claim(led.PriorityTimer)
	a := alarm.New(alarm.Options{
		Saved:   remember.NewSlot[[]alarm.Alarm](featureremember.Get(), "alarms"),
		Snoozes: remember.NewSlot[map[string]time.Time](featureremember.Get(), "alarm_snoozes"),
		Chime:   alerts.Get().RingAlarm,
		Duck: func() func() {
			sound := speaker.Sound()
			sound.Backgrounds().Duck(true)
			ringing.Play(led.EffectPulse, ringingColor)
			return func() {
				ringing.Clear()
				sound.Backgrounds().Duck(false)
			}
		},
		RingFor: alerts.Get().RingFor,
		Trusted: timesync.Get().Valid,
	})
	a.Told.Listen(component.Fire.Emit)
	a.Dawning.Listen(func(d alarm.Dawn) {
		if !d.Rising() {
			dawning.Clear()
			return
		}
		dawning.React(led.EffectRoomGlow, dawnColor, led.Room{
			Level: func() float64 { return a.Dawn().Share(time.Now()) },
		})
	})
	return a
})

func Get() *alarm.Alarms { return get() }
