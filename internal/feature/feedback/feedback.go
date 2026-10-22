package feedback

import (
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	sharedtone "github.com/ygelfand/libcountertop/pkg/audio/tone"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/tones"
	"github.com/ygelfand/echolocal/internal/hardware/indicate"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

func init() {
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(15))
}

const failureFlash = 1500 * time.Millisecond

var failureColor = led.Color{R: 0xC0, G: 0x00, B: 0x00}

type Feedback struct {
	*sharedtone.Feedback
	failure *esphome.Select
}

var (
	once   sync.Once
	shared *Feedback
)

func Get() *Feedback {
	once.Do(func() {
		shared = &Feedback{
			Feedback: sharedtone.NewFeedback(
				config.FeedbackSection,
				speaker.Sound(),
				component.DevicePlayback,
				sharedtone.FeedbackOptions{Sounds: tones.Get(), Hold: true},
			),
			failure: &esphome.Select{
				Base: esphome.Base{
					ObjectID: "failure_effect",
					DeviceID: component.DeviceRing,
					Name:     "Ring on failure",
					Icon:     "mdi:alert-circle",
					Category: esphome.CategoryConfig,
				},
			},
		}
		component.Settings.Add(shared.Controls)
		tones.Get().Changed.Listen(func(struct{}) { shared.Refresh() })
		component.BindEffect(shared.failure, led.EffectNames(), nil, config.Set().Ring().Trouble)
	})
	return shared
}

func (f *Feedback) Name() string { return "feedback" }

func (f *Feedback) Entities() []esphome.Entity {
	if !component.Board().Has(board.Ring) {
		return f.Feedback.Entities()
	}
	return append(f.Feedback.Entities(), f.failure)
}

func (f *Feedback) Restore(c config.Config) {
	f.Feedback.Restore()
	component.RestoreEffect(f.failure, c.Ring.Trouble, nil, config.Set().Ring().Trouble)
}

func Failure() {
	Get().Failure()

	name := config.Get().Ring.Trouble
	if name == "" {
		return
	}
	indicate.Get().Claim(indicate.PriorityTrouble).ShowFor(name, failureColor, failureFlash)
}

func Cancelled() { Get().Cancelled() }

func Preview(s config.Stream) { Get().Preview(s) }
