package voice

import (
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/light"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"
)

type turnEffects struct{ claim *led.Claim }

func (e *turnEffects) Begin() { e.claim = led.Get().Claim(led.PriorityTurn) }
func (e *turnEffects) End()   { e.claim.Release() }
func (e *turnEffects) Clear() { e.claim.Clear() }

// Elsewhere is silent (LED card t_f902c367): stock Alexa shows nothing when
// another device answers the wake word first, while the old WorkElsewhere
// flash was a Chase in 0x80,0x40,0xC0 — the "purple cycling" Jag saw on most
// wake words, because with two dots in earshot the twin almost always answers
// first ("Duplicate wake-up detected" in the 13:34-13:40 logcat). The busy
// Task machinery still drives the detection window; the turn-phase flash is
// dropped entirely.
func (e *turnEffects) Elsewhere() {}

// listenEffect resolves the effect the turn plays (LED card t_f902c367).
// While listening, libcountertop's Presentation.Phase plays the wake-word
// slot's configured effect; the deployed dots carry wake.words=null, so
// Wake.Slot() returns the DefaultWakeWord whose effect is "Pulse" — and
// Pulse takes the claim's base colour, forced white by the ring light being
// off (light.usable substitutes 1,1,1). That rendered the WHITE listen sweep.
// Remap ONLY the unconfigured default, and only while no wake word row exists
// at all: a wake word explicitly configured in wake.words keeps whatever
// effect its owner chose — including an explicit "Pulse" — because a
// configured slot arrives as a real Words row and falls outside this guard.
func listenEffect(name string, configuredWords int) string {
	if name == schema.DefaultEffect && configuredWords == 0 {
		return led.EffectBlueChase
	}
	return name
}

func (e *turnEffects) Play(name string, reversed bool) {
	name = listenEffect(name, len(config.WakeSection.Read().Words))
	base := light.Get().Base()
	if reversed {
		e.claim.PlayReversed(name, base)
	} else {
		e.claim.Play(name, base)
	}
}