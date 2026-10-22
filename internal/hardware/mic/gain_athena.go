//go:build board_athena

package mic

import (
	"fmt"
	"log/slog"

	"github.com/ygelfand/libcountertop/pkg/audio/alsa"
)

const gainMaxDB = 42

// As /vendor/etc/audio_device_tas5805m.xml sets up the TLV320ADC5140, in its order.
var converterSetup = []struct {
	name  string
	value uint32
}{
	{"ADC Reset A", 1},
	{"Ch1 Output A", 0},
	{"Ch1 Slot Offset A", 0},
	{"Ch2 Output A", 0},
	{"Ch2 Slot Offset A", 32},
	{"Ch3 Output A", 1},
	{"Ch3 Slot Offset A", 0},
	{"Ch4 Output A", 1},
	{"Ch4 Slot Offset A", 32},
	{"Enable Secondary Output A", 3},
	{"Ch1 Digital Capture Vol A", 201},
	{"Ch2 Digital Capture Vol A", 201},
	{"Ch3 Digital Capture Vol A", 201},
	{"Ch4 Digital Capture Vol A", 201},
	{"DRE Trigger Level A", 4},
	{"DRE Max Gain A", 11},
	{"Ch1 DRE Enable A", 0},
	{"Ch2 DRE Enable A", 0},
	{"Ch3 DRE Enable A", 0},
	{"Ch4 DRE Enable A", 0},
}

func routeInputs() {}

func setupConverter() {
	m, err := alsa.OpenMixer(Card)
	if err != nil {
		slog.Error("opening the mixer failed", "err", err)
		return
	}
	defer m.Close()

	for _, c := range converterSetup {
		if err := m.SetInt(c.name, c.value); err != nil {
			slog.Error("setting up the microphone converter failed", "control", c.name, "err", err)
		}
	}
}

func applyGain(db int) {
	m, err := alsa.OpenMixer(Card)
	if err != nil {
		slog.Error("opening the mixer failed", "err", err)
		return
	}
	defer m.Close()

	v := uint32(min(max(db, 0), gainMaxDB))
	for ch := 1; ch <= Mics; ch++ {
		name := fmt.Sprintf("Ch%d Gain A", ch)
		if err := m.SetInt(name, v); err != nil {
			slog.Error("setting the microphone gain failed", "control", name, "err", err)
			return
		}
	}
	slog.Info("microphone gain", "db", db)
}
