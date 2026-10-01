package loadtest

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficProfile_RPSAt(t *testing.T) {
	workloadDuration := 10 * time.Second
	tests := []struct {
		name    string
		profile TrafficProfile
		elapsed time.Duration
		want    float64
	}{
		{
			name:    "constant",
			profile: TrafficProfile{Type: "constant", RPS: 5},
			elapsed: 7 * time.Second,
			want:    5,
		},
		{
			name:    "ramp start",
			profile: TrafficProfile{Type: "ramp", FromRPS: 2, ToRPS: 10},
			elapsed: 0,
			want:    2,
		},
		{
			name:    "ramp midpoint",
			profile: TrafficProfile{Type: "ramp", FromRPS: 2, ToRPS: 10},
			elapsed: 5 * time.Second,
			want:    6,
		},
		{
			name:    "sine starts at minimum",
			profile: TrafficProfile{Type: "sine", MinRPS: 2, MaxRPS: 10, Period: "4s"},
			elapsed: 0,
			want:    2,
		},
		{
			name:    "sine reaches maximum halfway through the period",
			profile: TrafficProfile{Type: "sine", MinRPS: 2, MaxRPS: 10, Period: "4s"},
			elapsed: 2 * time.Second,
			want:    10,
		},
		{
			name:    "spikes use spike rate first",
			profile: TrafficProfile{Type: "spikes", BaseRPS: 2, SpikeRPS: 10, SpikeDuration: "1s", Interval: "4s"},
			elapsed: 500 * time.Millisecond,
			want:    10,
		},
		{
			name:    "spikes return to base rate",
			profile: TrafficProfile{Type: "spikes", BaseRPS: 2, SpikeRPS: 10, SpikeDuration: "1s", Interval: "4s"},
			elapsed: 2 * time.Second,
			want:    2,
		},
		{
			name: "stages select the active stage",
			profile: TrafficProfile{
				Type:   "stages",
				Stages: []TrafficStage{{Duration: "4s", RPS: 2}, {Duration: "6s", RPS: 8}},
			},
			elapsed: 6 * time.Second,
			want:    8,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.InDelta(t, test.want, test.profile.RPSAt(test.elapsed, workloadDuration), 0.0001)
		})
	}
}

func TestTrafficProfile_Validate(t *testing.T) {
	workloadDuration := 10 * time.Second
	tests := []struct {
		name    string
		profile TrafficProfile
		wantErr string
	}{
		{name: "default closed loop"},
		{name: "constant", profile: TrafficProfile{Type: "constant", RPS: 5}},
		{name: "ramp", profile: TrafficProfile{Type: "ramp", FromRPS: 2, ToRPS: 10}},
		{name: "sine", profile: TrafficProfile{Type: "sine", MinRPS: 2, MaxRPS: 10, Period: "2s"}},
		{name: "spikes", profile: TrafficProfile{Type: "spikes", BaseRPS: 2, SpikeRPS: 10, SpikeDuration: "1s", Interval: "4s"}},
		{name: "stages", profile: TrafficProfile{Type: "stages", Stages: []TrafficStage{{Duration: "4s", RPS: 2}, {Duration: "6s", RPS: 8}}}},
		{name: "unknown type", profile: TrafficProfile{Type: "random"}, wantErr: "unsupported type"},
		{name: "zero constant rate", profile: TrafficProfile{Type: "constant"}, wantErr: "rps must be a positive"},
		{name: "invalid spike duration", profile: TrafficProfile{Type: "spikes", BaseRPS: 2, SpikeRPS: 10, SpikeDuration: "5s", Interval: "4s"}, wantErr: "must not exceed"},
		{name: "partial stages", profile: TrafficProfile{Type: "stages", Stages: []TrafficStage{{Duration: "4s", RPS: 2}}}, wantErr: "must equal workload duration"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.profile.Validate(workloadDuration)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestTrafficProfile_RPSAtIsFinite(t *testing.T) {
	profile := TrafficProfile{Type: "sine", MinRPS: 2, MaxRPS: 10, Period: "3s"}
	for elapsed := time.Duration(0); elapsed < 15*time.Second; elapsed += 100 * time.Millisecond {
		require.False(t, math.IsNaN(profile.RPSAt(elapsed, 15*time.Second)))
	}
}
