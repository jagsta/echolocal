//go:build board_athena

package alsps

import (
	"math"
	"testing"
)

func TestLuxMatchesTheVendorHAL(t *testing.T) {
	cal := calibration{
		factors: [4]float64{1.07039, 1.09634, 1.08034, 1.03962},
		itime:   700,
		gain:    64,
		stack:   stack1,
	}
	for _, c := range []struct {
		rgbc [4]float64
		want float64
	}{
		{[4]float64{562, 389, 359, 702}, 4.93954},
		{[4]float64{4646, 3037, 2171, 9509}, 143.149},
		{[4]float64{1967, 1297, 982, 3700}, 50.226},
		{[4]float64{665, 454, 403, 919}, 7.536},
		{[4]float64{768, 520, 448, 1144}, 10.020},
	} {
		got := cal.lux(c.rgbc, 700, 64)
		if math.Abs(got-c.want)/c.want > 1e-4 {
			t.Errorf("lux(%v) = %.4f, want %.4f", c.rgbc, got, c.want)
		}
	}
}
