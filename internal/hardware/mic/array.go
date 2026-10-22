//go:build !board_checkers && !board_cronos && !board_crown && !board_athena

package mic

// The capture codec accepts one format only: 16 kHz, S24_3LE, 9 channels.
const (
	Channels      = 9
	CaptureDevice = 24
)

// ch7 and ch8 are the playback loopback.
const Mics = 7

const CenterMic = 6

// adcs are the four converters the seven microphones arrive on.
var adcs = []string{"A", "B", "C", "D"}

const beamforming = true

const lowCutHz = 0
