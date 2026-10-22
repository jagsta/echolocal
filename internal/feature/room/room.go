package room

import (
	"slices"
	"sync"

	esphome "github.com/ygelfand/go-esphome-device"

	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/light"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
)

func init() {
	component.Register(
		sharedcomponent.Device,
		Get,
		sharedcomponent.Order(20),
		component.Needs(board.Ring),
	)
}

type Reaction struct {
	sel   *esphome.Select
	claim *led.Claim

	room led.Room
	base func() led.Color
}

var (
	once   sync.Once
	shared *Reaction
)

func Get() *Reaction {
	once.Do(func() { shared = build() })
	return shared
}

func build() *Reaction {
	r := &Reaction{
		sel: &esphome.Select{
			Base: esphome.Base{
				ObjectID: "room_reaction",
				DeviceID: component.DeviceRing,
				Name:     "Ring follows the room",
				Icon:     "mdi:waveform",
				Category: esphome.CategoryConfig,
			},
		},
		base:  light.Get().Base,
		claim: led.Get().Claim(led.PriorityRoom),
	}

	source := mic.Get()
	r.room = led.Room{Level: source.Level, Facing: source.Facing, Beam: source.BeamFacing}

	names := led.Names(led.KindRoom)
	if !mic.FollowsBeam() {
		names = slices.DeleteFunc(names, func(n string) bool { return n == led.EffectBeamCompass })
	}
	component.BindEffect(r.sel, names, r.show, config.Set().Ring().Reaction)

	light.Get().OnColor(r.recolour)
	return r
}

func (r *Reaction) Name() string { return "room reaction" }

func (r *Reaction) Entities() []esphome.Entity { return []esphome.Entity{r.sel} }

func (r *Reaction) Restore(c config.Config) {
	component.RestoreEffect(r.sel, c.Ring.Reaction, r.show, config.Set().Ring().Reaction)
}

func (r *Reaction) show(name string) {
	if name == "" {
		r.claim.Clear()
		return
	}
	r.claim.React(name, r.base(), r.room)
}

func (r *Reaction) recolour() { r.show(config.Get().Ring.Reaction) }
