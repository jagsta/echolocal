package mic

import "math"

// Degrees clockwise from segment 0 that beam 0 faces, found by pointing the array at sources.
const ringOffset = 320.0

func (s *Source) Facing() (float64, bool) {
	s.wantFacing.Store(true)

	beam := s.facing.Load()
	if beam < 0 {
		return 0, false
	}

	return ringTurn(float64(beam) * (360.0 / Beams)), true
}

func ringTurn(deg float64) float64 {
	return math.Mod(math.Mod((deg-ringOffset)/360, 1)+1, 1)
}

func (s *Source) findFacing(mics [][]int16) {
	if !s.wantFacing.Swap(false) {
		return
	}

	s.finder.Look(mics)
	s.facing.Store(int32(s.finder.Beam()))
}
