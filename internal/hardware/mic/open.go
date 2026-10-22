//go:build !board_athena

package mic

import "github.com/ygelfand/libcountertop/pkg/audio/alsa"

func openCapture() (stream, error) {
	return alsa.Open(Card, CaptureDevice, alsa.Config{
		Channels:   Channels,
		Rate:       Rate,
		Format:     alsa.FormatS24_3LE,
		Bits:       Bits,
		PeriodSize: period,
		Periods:    periods,
	})
}
