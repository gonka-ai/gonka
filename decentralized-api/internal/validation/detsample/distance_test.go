package detsample

import (
	"math"
	"testing"
)

var distPos0 = map[string]float64{"5": -0.5, "10": -1.2, "2": -2.5, "100": -3.9, "7": -4.1}
var distPos1 = map[string]float64{"3": -0.3, "42": -1.1, "9": -2.2, "11": -3.0, "1": -4.5}

func TestMAEDistanceIdenticalIsZero(t *testing.T) {
	if d := MAEDistance([]map[string]float64{distPos0}, []map[string]float64{distPos0}); d != 0.0 {
		t.Errorf("MAEDistance identical = %v, want 0", d)
	}
}

func TestMAEDistanceUniformShift(t *testing.T) {
	shifted := make(map[string]float64, len(distPos0))
	for tid, v := range distPos0 {
		shifted[tid] = v - 0.5
	}
	d := MAEDistance([]map[string]float64{distPos0}, []map[string]float64{shifted})
	if math.Abs(d-0.5) > 1e-9 {
		t.Errorf("MAEDistance uniform 0.5 shift = %v, want 0.5", d)
	}
}

func TestMAEDistanceLengthMismatchIsMax(t *testing.T) {
	d := MAEDistance(
		[]map[string]float64{distPos0, distPos1},
		[]map[string]float64{distPos0})
	if d != 10.0 {
		t.Errorf("MAEDistance length mismatch = %v, want 10", d)
	}
}

func TestMAEDistanceMissingTokenPenalized(t *testing.T) {
	partial := map[string]float64{"5": -0.5, "10": -1.2, "2": -2.5, "100": -3.9} // one token dropped
	d := MAEDistance([]map[string]float64{distPos0}, []map[string]float64{partial})
	if d <= Stage2MAEFraudThreshold {
		t.Errorf("MAEDistance missing token = %v, want > %v", d, Stage2MAEFraudThreshold)
	}
}
