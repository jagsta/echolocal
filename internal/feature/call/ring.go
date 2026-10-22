//go:build board_biscuit

package call

import (
	"sync"

	esphome "github.com/ygelfand/go-esphome-device"

	sharedcall "github.com/ygelfand/libcountertop/pkg/media/call"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/light"
	"github.com/ygelfand/echolocal/internal/feature/mute"
	"github.com/ygelfand/echolocal/internal/hardware/led"
)

func init() {
	component.Register(
		sharedcomponent.Device,
		ringGet,
		sharedcomponent.Order(21),
		component.Needs(board.Ring),
	)
}

func front(d *sharedcall.Dependencies) {
	d.Privacy = muted{}
	if component.Board().Has(board.Ring) {
		d.Stage = ringGet()
	}
}

func configureSettings(c *sharedcall.Calls) {
	c.Controls().Table = c.Controls().Table.Without("stream")
}

type muted struct{}

func (muted) MicMuted() bool {
	on, _ := mute.Get().Muted()
	return on
}

func (muted) Watch(changed func()) func() {
	return mute.Get().Changed.Listen(func(bool) { changed() })
}

type ring struct {
	incoming *esphome.Select
	talking  *esphome.Select
	claim    *led.Claim

	mu    sync.Mutex
	shown string
}

var (
	ringOnce sync.Once
	rings    *ring
)

func ringGet() *ring {
	ringOnce.Do(func() { rings = buildRing() })
	return rings
}

func buildRing() *ring {
	r := &ring{
		incoming: &esphome.Select{
			Base: esphome.Base{
				ObjectID: "ring_incoming_call",
				DeviceID: component.DeviceRing,
				Name:     "Ring for an incoming call",
				Icon:     "mdi:phone-incoming",
				Category: esphome.CategoryConfig,
			},
		},
		talking: &esphome.Select{
			Base: esphome.Base{
				ObjectID: "ring_in_call",
				DeviceID: component.DeviceRing,
				Name:     "Ring during a call",
				Icon:     "mdi:phone-in-talk",
				Category: esphome.CategoryConfig,
			},
		},
		claim: led.Get().Claim(led.PriorityAlarm),
	}
	component.BindEffect(r.incoming, led.EffectNames(), r.changed, config.Set().Ring().Incoming)
	component.BindEffect(r.talking, led.EffectNames(), r.changed, config.Set().Ring().Talking)
	return r
}

func (r *ring) Name() string { return "call ring" }

func (r *ring) Entities() []esphome.Entity { return []esphome.Entity{r.incoming, r.talking} }

func (r *ring) Restore(c config.Config) {
	component.RestoreEffect(r.incoming, c.Ring.Incoming, nil, config.Set().Ring().Incoming)
	component.RestoreEffect(r.talking, c.Ring.Talking, nil, config.Set().Ring().Talking)
}

func (r *ring) Hold() func() { return r.Redraw }

func (r *ring) Redraw() { r.show(false) }

func (r *ring) changed(string) { r.show(true) }

func (r *ring) show(force bool) {
	var effect string
	if c, ok := Get().Now(); ok {
		switch c.State {
		case sharedcall.Ringing:
			effect = r.incoming.Get()
		case sharedcall.Calling, sharedcall.Talking:
			effect = r.talking.Get()
		}
	}
	if effect == component.EffectNone {
		effect = ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if effect == r.shown && !force {
		return
	}
	r.shown = effect
	if effect == "" {
		r.claim.Clear()
		return
	}
	r.claim.Play(effect, light.Get().Base())
}
