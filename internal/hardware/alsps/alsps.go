//go:build !board_athena

package alsps

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ygelfand/libcountertop/pkg/system/metrics"

	"github.com/ygelfand/libcountertop/pkg/input"
)

var current atomic.Pointer[Sensor]

func Ambient() metrics.Reading {
	s := current.Load()
	if s == nil {
		return metrics.Reading{}
	}
	lux, ok := s.Lux()
	return metrics.Reading{Value: lux, Known: ok}
}

const (
	control = "/sys/class/misc/m_alsps_misc"
	device  = "m_alsps_input"

	// The vendor daemon's report period.
	period = 500 * time.Millisecond
)

type Sensor struct {
	dev   *input.Device
	lux   atomic.Uint64
	known atomic.Bool
}

func Open() (*Sensor, error) {
	devices, err := input.List()
	if err != nil {
		return nil, err
	}
	var dev *input.Device
	for _, d := range devices {
		if d.Name == device && dev == nil {
			dev = d
			continue
		}
		_ = d.Close()
	}
	if dev == nil {
		return nil, errors.New("alsps: no " + device)
	}

	if err := write("alsdelay", strconv.FormatInt(period.Nanoseconds(), 10)); err != nil {
		_ = dev.Close()
		return nil, err
	}
	if err := write("alsactive", "1"); err != nil {
		_ = dev.Close()
		return nil, err
	}

	s := &Sensor{dev: dev}
	if v, err := dev.Abs(input.AbsX); err == nil {
		s.set(float64(v))
	}
	current.Store(s)
	return s, nil
}

func (s *Sensor) Run(ctx context.Context) error {
	for {
		e, err := s.dev.Read()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("alsps: reading: %w", err)
		}
		if e.Type == input.EvAbs && e.Code == input.AbsX {
			s.set(float64(e.Value))
		}
	}
}

func (s *Sensor) Lux() (float64, bool) {
	return math.Float64frombits(s.lux.Load()), s.known.Load()
}

func (s *Sensor) Close() error {
	current.CompareAndSwap(s, nil)
	err := write("alsactive", "0")
	if cerr := s.dev.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *Sensor) set(lux float64) {
	s.lux.Store(math.Float64bits(lux))
	s.known.Store(true)
}

func write(name, value string) error {
	if err := os.WriteFile(control+"/"+name, []byte(value), 0o644); err != nil {
		return fmt.Errorf("alsps: %w", err)
	}
	return nil
}
