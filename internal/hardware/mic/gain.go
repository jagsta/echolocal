//go:build !board_athena

package mic

import (
	"fmt"
	"log/slog"

	"github.com/ygelfand/libcountertop/pkg/audio/alsa"
)

const (
	// One control per ADC, each carrying both of its channels.
	pgaControls = "ADC_%s MICPGA Volume Ctrl"

	// The mixer declares a maximum of 80; the 7-bit register takes up to 119 (59.5 dB) and wraps above 127.
	pgaStepDB = 0.5
	pgaMax    = 119
)

func routeInputs() {
	m, err := alsa.OpenMixer(Card)
	if err != nil {
		slog.Error("opening the mixer failed", "err", err)
		return
	}
	defer m.Close()

	for _, adc := range adcs {
		for name, v := range map[string]uint32{
			fmt.Sprintf("ADC_%s DIF1_L Input Gain", adc):                          0,
			fmt.Sprintf("ADC_%s DIF1_R Input Gain", adc):                          0,
			fmt.Sprintf("ADC_%[1]s Left Ip Select ADC_%[1]s DIF1_L switch", adc):  1,
			fmt.Sprintf("ADC_%[1]s Right Ip Select ADC_%[1]s DIF1_R switch", adc): 1,
		} {
			if err := m.SetInt(name, v); err != nil {
				slog.Error("routing the microphone input failed", "control", name, "err", err)
			}
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

	steps := min(max(float64(db)/pgaStepDB, 0), pgaMax)
	for _, adc := range adcs {
		name := fmt.Sprintf(pgaControls, adc)
		if err := m.SetInt(name, uint32(steps)); err != nil {
			slog.Error("setting the microphone gain failed", "control", name, "err", err)
			return
		}
	}
	slog.Info("microphone gain", "db", db)
}
