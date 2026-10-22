package mic

import (
	"math"
	"sync"
	"time"

	"github.com/ygelfand/echolocal/internal/lib/subband"
)

const compassLease = time.Second

var bearings = sync.OnceValue(func() []float64 {
	w := vendor()
	if w == nil {
		return nil
	}
	return w.Bearings(ringDelay)
})

func FollowsBeam() bool { return vendor() != nil }

func (s *Source) BeamFacing() (float64, bool) {
	s.compassAsked.Store(time.Now().UnixNano())

	beam := s.compassBeam.Load()
	toward := bearings()
	if beam < 0 || int(beam) >= len(toward) {
		return 0, false
	}
	return ringTurn(toward[beam] * 180 / math.Pi), true
}

func (s *Source) followBeam(mics [][]int16) {
	if time.Since(time.Unix(0, s.compassAsked.Load())) > compassLease {
		s.compass = nil
		s.compassBeam.Store(-1)
		return
	}
	if b, ok := s.mixer.(*subband.Beamformer); ok {
		s.compass = nil
		s.compassBeam.Store(int32(b.Beam()))
		return
	}
	if s.compass == nil {
		w := vendor()
		if w == nil {
			return
		}
		s.compass = w.New()
	}
	s.compass.Look(mics)
	s.compassBeam.Store(int32(s.compass.Beam()))
}

func ringDelay(channel int, bearing float64) float64 {
	late := ringRadius / speedOfSound * Rate
	if channel == CenterMic {
		return late
	}
	return late * (1 - math.Cos(bearing-(firstMic+float64(channel)*micSpacing)))
}
