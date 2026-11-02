package loadtest

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const defaultTrafficType = "closed_loop"

// ResolvedType returns closed_loop for legacy scenarios that omit traffic.type.
func (p TrafficProfile) ResolvedType() string {
	if strings.TrimSpace(p.Type) == "" {
		return defaultTrafficType
	}
	return p.Type
}

func (p TrafficProfile) Validate(workloadDuration time.Duration) error {
	switch p.ResolvedType() {
	case "closed_loop":
		return nil
	case "constant":
		return validateRPS("rps", p.RPS)
	case "ramp":
		if err := validateRPS("from_rps", p.FromRPS); err != nil {
			return err
		}
		return validateRPS("to_rps", p.ToRPS)
	case "sine":
		if err := validateRPS("min_rps", p.MinRPS); err != nil {
			return err
		}
		if err := validateRPS("max_rps", p.MaxRPS); err != nil {
			return err
		}
		if p.MaxRPS < p.MinRPS {
			return fmt.Errorf("max_rps must be at least min_rps")
		}
		_, err := positiveDuration("period", p.Period)
		return err
	case "spikes":
		if err := validateRPS("base_rps", p.BaseRPS); err != nil {
			return err
		}
		if err := validateRPS("spike_rps", p.SpikeRPS); err != nil {
			return err
		}
		if p.SpikeRPS <= p.BaseRPS {
			return fmt.Errorf("spike_rps must exceed base_rps")
		}
		spikeDuration, err := positiveDuration("spike_duration", p.SpikeDuration)
		if err != nil {
			return err
		}
		interval, err := positiveDuration("interval", p.Interval)
		if err != nil {
			return err
		}
		if spikeDuration > interval {
			return fmt.Errorf("spike_duration must not exceed interval")
		}
		return nil
	case "stages":
		if len(p.Stages) == 0 {
			return fmt.Errorf("stages must not be empty")
		}
		var total time.Duration
		for i, stage := range p.Stages {
			if err := validateRPS(fmt.Sprintf("stages[%d].rps", i), stage.RPS); err != nil {
				return err
			}
			duration, err := positiveDuration(fmt.Sprintf("stages[%d].duration", i), stage.Duration)
			if err != nil {
				return err
			}
			total += duration
		}
		if total != workloadDuration {
			return fmt.Errorf("stage durations %s must equal workload duration %s", total, workloadDuration)
		}
		return nil
	default:
		return fmt.Errorf("unsupported type %q", p.Type)
	}
}

// RPSAt returns the desired request rate at elapsed time. It is only valid for
// rate-based profiles; closed_loop is driven by completed requests instead.
func (p TrafficProfile) RPSAt(elapsed, workloadDuration time.Duration) float64 {
	switch p.ResolvedType() {
	case "constant":
		return p.RPS
	case "ramp":
		progress := math.Min(1, math.Max(0, float64(elapsed)/float64(workloadDuration)))
		return p.FromRPS + (p.ToRPS-p.FromRPS)*progress
	case "sine":
		period, _ := time.ParseDuration(p.Period)
		phase := 2 * math.Pi * float64(elapsed%period) / float64(period)
		return p.MinRPS + (p.MaxRPS-p.MinRPS)*(1-math.Cos(phase))/2
	case "spikes":
		interval, _ := time.ParseDuration(p.Interval)
		spikeDuration, _ := time.ParseDuration(p.SpikeDuration)
		if elapsed%interval < spikeDuration {
			return p.SpikeRPS
		}
		return p.BaseRPS
	case "stages":
		remaining := elapsed
		for _, stage := range p.Stages {
			duration, _ := time.ParseDuration(stage.Duration)
			if remaining < duration {
				return stage.RPS
			}
			remaining -= duration
		}
		return p.Stages[len(p.Stages)-1].RPS
	default:
		return 0
	}
}

func (p TrafficProfile) IsRateBased() bool {
	return p.ResolvedType() != defaultTrafficType
}

func validateRPS(name string, value float64) error {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%s must be a positive finite number", name)
	}
	return nil
}

func positiveDuration(name, raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		if err != nil {
			return 0, fmt.Errorf("%s: %w", name, err)
		}
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return duration, nil
}
