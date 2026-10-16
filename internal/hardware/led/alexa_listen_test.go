package led

import "testing"

// The stock Alexa colour cards (LED card t_05fc05ad):

func TestWorkWakeWordShowsTheAlexaListenEffect(t *testing.T) {
	effect, _ := WorkWakeWord.appearance()
	if effect != EffectBlueChase {
		t.Fatalf("the listen indication plays %q, want %q (stock Alexa blue)", effect, EffectBlueChase)
	}
}

func TestAlexaListenEffectIsBlueWhateverTheRingBase(t *testing.T) {
	built := NewDriver(nil).Busy()
	busyFixture := built
	task := busyFixture.Start(WorkWakeWord)
	defer task.Done()

	content := built.claim.get()
	if content.Effect != EffectBlueChase {
		t.Fatalf("busy claim plays %q, want %q", content.Effect, EffectBlueChase)
	}

	// The catalogue entry is palette-driven, so the claim's base colour must
	// be empty — effect() ignores the base and renders the fixed blue palette.
	if content.Base != (Color{}) {
		t.Fatalf("listen claim carries base %+v; the Alexa blue palette must own the colour", content.Base)
	}

	e, ok := byName[EffectBlueChase]
	if !ok {
		t.Fatalf("%q missing from the catalogue", EffectBlueChase)
	}
	if len(e.Palette) == 0 {
		t.Fatalf("%q has no palette; it would take the claim's base colour", EffectBlueChase)
	}
	for _, c := range e.Palette {
		if c.B == 0 || c.B <= c.G {
			t.Fatalf("palette colour %+v is not blue", c)
		}
	}
	if len(e.Palette) != len(alexaBlue) {
		t.Fatalf("palette is not the Alexa blue set")
	}
	for i, c := range e.Palette {
		if c != alexaBlue[i] {
			t.Fatalf("palette[%d] = %+v, want the Alexa blue %+v", i, c, alexaBlue[i])
		}
	}
}

func TestAlexaListenIsOfferedByEffectNames(t *testing.T) {
	for _, name := range EffectNames() {
		if name == EffectBlueChase {
			return
		}
	}
	t.Fatalf("%q is not offered; settings UI cannot pick it", EffectBlueChase)
}