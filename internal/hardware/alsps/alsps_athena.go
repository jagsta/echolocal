//go:build board_athena

package alsps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ygelfand/libcountertop/pkg/system/metrics"
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
	sensorName = "tcs3400_als"
	inputs     = "/sys/class/input"
	calPath    = "/mnt/vendor/nvcfg/sensor/als_cal.json"

	period = 700 * time.Millisecond
)

// sensors.athena.so's calibrateData2Point table for stackIndex 1, matched to its output on athena.
var stack1 = stack{
	low:    [4]float64{-0.802149, -0.773151, -0.819355, 0.812086},
	high:   [4]float64{-0.247696, -0.251481, -0.282065, 0.267532},
	lowIR:  0.506,
	highIR: 0.701,
	irDiv:  2.01,
}

type stack struct {
	low, high     [4]float64
	lowIR, highIR float64
	irDiv         float64
}

type calibration struct {
	factors     [4]float64
	itime, gain float64
	stack       stack
}

type Sensor struct {
	dir   string
	cal   calibration
	lux   atomic.Uint64
	known atomic.Bool
}

func Open() (*Sensor, error) {
	dir, err := find()
	if err != nil {
		return nil, err
	}
	cal, err := loadCalibration()
	if err != nil {
		return nil, err
	}
	s := &Sensor{dir: dir, cal: cal}
	if lux, err := s.read(); err == nil {
		s.set(lux)
	}
	current.Store(s)
	return s, nil
}

func (s *Sensor) Run(ctx context.Context) error {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			lux, err := s.read()
			if err != nil {
				return fmt.Errorf("alsps: reading: %w", err)
			}
			s.set(lux)
		}
	}
}

func (s *Sensor) Lux() (float64, bool) {
	return math.Float64frombits(s.lux.Load()), s.known.Load()
}

func (s *Sensor) Close() error {
	current.CompareAndSwap(s, nil)
	return nil
}

func (s *Sensor) set(lux float64) {
	s.lux.Store(math.Float64bits(lux))
	s.known.Store(true)
}

func (s *Sensor) read() (float64, error) {
	var raw [6]float64
	for i, name := range []string{"als_red", "als_green", "als_blue", "als_clear", "als_itime", "als_gain"} {
		v, err := s.number(name)
		if err != nil {
			return 0, err
		}
		raw[i] = v
	}
	return s.cal.lux([4]float64(raw[:4]), raw[4], raw[5]), nil
}

func (c calibration) lux(rgbc [4]float64, itime, gain float64) float64 {
	if itime <= 0 || gain <= 0 {
		return 0
	}
	scale := c.itime * c.gain / (itime * gain)
	var ch [4]float64
	for i := range ch {
		ch[i] = rgbc[i] * scale * c.factors[i]
	}
	ir := (ch[0] + ch[1] + ch[2] - ch[3]) / c.stack.irDiv

	var t float64
	if ch[1] != 0 {
		t = min(max((ir/ch[1]-c.stack.lowIR)/(c.stack.highIR-c.stack.lowIR), 0), 1)
	}
	w := t * t * t * (10 + t*(-15+6*t))

	var low, high float64
	for i := range ch {
		low += c.stack.low[i] * (ch[i] - ir)
		high += c.stack.high[i] * (ch[i] - ir)
	}
	return max(w*high+(1-w)*low, 0)
}

func (s *Sensor) number(name string) (float64, error) {
	b, err := os.ReadFile(s.dir + "/" + name)
	if err != nil {
		return 0, err
	}
	field, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	return strconv.ParseFloat(field, 64)
}

func find() (string, error) {
	entries, err := os.ReadDir(inputs)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		dir := inputs + "/" + e.Name()
		name, err := os.ReadFile(dir + "/name")
		if err == nil && strings.TrimSpace(string(name)) == sensorName {
			return dir, nil
		}
	}
	return "", errors.New("alsps: no " + sensorName)
}

func loadCalibration() (calibration, error) {
	b, err := os.ReadFile(calPath)
	if err != nil {
		return calibration{}, fmt.Errorf("alsps: %w", err)
	}
	var file struct {
		SensorCalData []struct {
			StackIndex int `json:"stackIndex"`
			CalData    struct {
				LowIRCF []float64 `json:"lowIRCF"`
			} `json:"calData"`
			CalRawData struct {
				LowIR []float64 `json:"lowIR"`
			} `json:"calRawData"`
		} `json:"sensorCalData"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return calibration{}, fmt.Errorf("alsps: %s: %w", calPath, err)
	}
	if len(file.SensorCalData) == 0 {
		return calibration{}, fmt.Errorf("alsps: %s has no sensor", calPath)
	}
	d := file.SensorCalData[0]
	if d.StackIndex != 1 {
		return calibration{}, fmt.Errorf("alsps: stackIndex %d has no table", d.StackIndex)
	}
	if len(d.CalData.LowIRCF) < 4 || len(d.CalRawData.LowIR) < 6 {
		return calibration{}, fmt.Errorf("alsps: %s is incomplete", calPath)
	}
	return calibration{
		factors: [4]float64(d.CalData.LowIRCF[:4]),
		itime:   d.CalRawData.LowIR[4],
		gain:    d.CalRawData.LowIR[5],
		stack:   stack1,
	}, nil
}
