//go:build board_checkers || board_cronos || board_crown || board_athena

package speaker

import (
	"slices"
	"testing"
)

func TestEachShowFindsItsAmp(t *testing.T) {
	for want, controls := range map[string][]string{
		"rt5616":             {"Ext_Speaker_Amp_Switch", "DAC1 Playback Volume", "OUT Playback Volume", "ADC_A MICPGA Volume Ctrl"},
		"tas5805m":           {"Ext_Speaker_Amp_Switch", "Master TAS5805m Playback Volume A", "TAS5805m EQ Enable", "ADC_A MICPGA Volume Ctrl"},
		"tas5805m-amplifier": {"Ext_Speaker_Amp_Switch", "Master TAS5805m Playback Volume", "TAS5805m LPM mode", "Ch1 Gain A"},
	} {
		got, ok := whichShowAmp(func(c string) bool { return slices.Contains(controls, c) })
		if !ok || got.name != want {
			t.Errorf("found %q (%t), want %q", got.name, ok, want)
		}
	}
	if a, ok := whichShowAmp(func(string) bool { return false }); ok {
		t.Errorf("a mixer with none of them found %q", a.name)
	}
}

func TestEveryShowAmpMarksItselfWithAControlItSets(t *testing.T) {
	for _, a := range showAmps {
		if !slices.ContainsFunc(a.init, func(k kctl) bool { return k.name == a.marker }) {
			t.Errorf("%s is found by %q, which its settings never touch", a.name, a.marker)
		}
	}
}
