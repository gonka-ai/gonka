package limits

import (
	"math"
	"testing"
)

// Test flow:
//  1. Build a table of weight and per-10000-weight-share combinations, including zero, negative, NaN and Inf inputs.
//  2. For each case, call `weightConcurrencyLimit` and assert it returns the expected concurrency, flooring fractional results and disabling on invalid input.
func TestWeightConcurrencyLimit(t *testing.T) {
	tests := []struct {
		name     string
		weight   float64
		per10000 float64
		want     int64
	}{
		{"disabled when per10000 is zero", 30000, 0, 0},
		{"disabled when per10000 is negative", 30000, -5, 0},
		{"disabled when weight is zero", 0, 5, 0},
		{"disabled when weight is negative", -10, 5, 0},
		{"normal case", 30000, 5, 15},
		{"floors a fractional result", 1000, 15, 1},
		{"NaN weight disables", math.NaN(), 5, 0},
		{"Inf per10000 disables", 30000, math.Inf(1), 0},
		{"Inf weight disables", math.Inf(1), 5, 0},
		{"NaN per10000 disables", 30000, math.NaN(), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := weightConcurrencyLimit(tt.weight, tt.per10000); got != tt.want {
				t.Errorf("weightConcurrencyLimit(%v, %v) = %d, want %d", tt.weight, tt.per10000, got, tt.want)
			}
		})
	}
}

// Test flow:
//  1. Build a table of current-available and full weight pairs, including zero and negative baselines.
//  2. For each case, call `scaleFactor` and assert it clamps to [0,1] and treats a non-positive baseline as unlimited.
func TestScaleFactor(t *testing.T) {
	tests := []struct {
		name             string
		currentAvailable float64
		full             float64
		want             float64
	}{
		{"half capacity", 50, 100, 0.5},
		{"clamps above one", 150, 100, 1},
		{"clamps negative to zero", -10, 100, 0},
		{"zero baseline means unlimited", 50, 0, 1},
		{"negative baseline means unlimited", 50, -5, 1},
		{"zero current", 0, 100, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := scaleFactor(tt.currentAvailable, tt.full); got != tt.want {
				t.Errorf("scaleFactor(%v, %v) = %v, want %v", tt.currentAvailable, tt.full, got, tt.want)
			}
		})
	}
}

// Test flow:
//  1. Build a table of current weights, host shares and an optional availability filter, covering hosts missing from one side or the other.
//  2. For each case, call `escrowWeight` and assert it sums the current weight of hosts that are both members and available, varying the table's inputs.
func TestEscrowWeight(t *testing.T) {
	tests := []struct {
		name           string
		currentWeights map[string]float64
		hostShares     map[string]float64
		available      func(string) bool
		want           float64
	}{
		{
			name:           "shares sum to the hosts' participation",
			currentWeights: map[string]float64{"hostA": 100, "hostB": 200},
			hostShares:     map[string]float64{"hostA": 0.5, "hostB": 0.25},
			want:           100,
		},
		{
			name:           "availability filter drops a host",
			currentWeights: map[string]float64{"hostA": 100, "hostB": 200},
			hostShares:     map[string]float64{"hostA": 0.5, "hostB": 0.25},
			available:      func(host string) bool { return host != "hostB" },
			want:           50,
		},
		{
			name:           "nil available treats every host as available",
			currentWeights: map[string]float64{"hostA": 40},
			hostShares:     map[string]float64{"hostA": 1},
			want:           40,
		},
		{
			name:           "empty maps yield zero",
			currentWeights: map[string]float64{},
			hostShares:     map[string]float64{},
			want:           0,
		},
		{
			name:           "host in currentWeights but not membership contributes nothing",
			currentWeights: map[string]float64{"hostA": 100, "hostC": 50},
			hostShares:     map[string]float64{"hostA": 1},
			want:           100,
		},
		{
			name:           "host in membership but not currentWeights contributes nothing",
			currentWeights: map[string]float64{"hostA": 100},
			hostShares:     map[string]float64{"hostA": 1, "hostD": 1},
			want:           100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := escrowWeight(tt.currentWeights, tt.hostShares, tt.available); got != tt.want {
				t.Errorf("escrowWeight(%v, %v) = %v, want %v", tt.currentWeights, tt.hostShares, got, tt.want)
			}
		})
	}
}
