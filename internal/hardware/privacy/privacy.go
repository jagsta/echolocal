package privacy

import (
	"os"
	"time"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/hardware/buttons"
)

type Mute interface {
	Get() (bool, error)
	Set(muted bool) error
	Toggle() (bool, error)

	HardwareToggles() bool

	Lag() time.Duration
}

type LED interface {
	SetBright(bright bool) error
	Bright() (bool, error)
}

func Microphone() (Mute, error) {
	if dir := component.Board().MuteDir; dir != "" {
		return gating{dir: dir, sw: buttons.Get().MuteSwitch}, nil
	}
	if present(state) {
		return driver{}, nil
	}
	return exported()
}

func CutsCamera() bool {
	if !component.Board().Has(board.MicMutesCamera) {
		return false
	}
	m, err := Microphone()
	if err != nil {
		return false
	}
	cut, err := m.Get()
	return err == nil && cut
}

func Light() (LED, error) {
	if present(brightness) {
		return driverLED{}, nil
	}
	return exportedLED()
}

func present(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
