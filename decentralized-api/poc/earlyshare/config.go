package earlyshare

// Mode controls how the early-share guard behaves.
type Mode string

const (
	// ModeDisabled performs no capture and has no validation effect.
	ModeDisabled Mode = "disabled"
	// ModeObserve captures checkpoints and logs pass/fail decisions but never
	// changes the vote.
	ModeObserve Mode = "observe"
	// ModeEnforce votes no after the miss-streak rule triggers or the early
	// inclusion proof check fails.
	ModeEnforce Mode = "enforce"
)

// Config holds the runtime configuration for the early-share guard.
type Config struct {
	Mode                  Mode
	FirstFraction         float64
	ThresholdRatio        float64
	RequireInclusionProof bool
	InclusionSampleSize   int
}

// Defaults for the guard. The guard ships in enforce mode; operators can opt
// out by explicitly setting mode to "observe" or "disabled" in the DAPI config.
const (
	DefaultFirstFraction       = 1.0 / 3.0
	DefaultThresholdRatio      = 0.5
	DefaultInclusionSampleSize = 5
)

// DefaultConfig returns the enforce-by-default configuration.
func DefaultConfig() Config {
	return Config{
		Mode:                  ModeEnforce,
		FirstFraction:         DefaultFirstFraction,
		ThresholdRatio:        DefaultThresholdRatio,
		RequireInclusionProof: true,
		InclusionSampleSize:   DefaultInclusionSampleSize,
	}
}

// Normalized fills zero/invalid fields with defaults and clamps ranges.
func (c Config) Normalized() Config {
	out := c
	switch out.Mode {
	case ModeObserve, ModeEnforce, ModeDisabled:
		// keep
	default:
		out.Mode = ModeDisabled
	}
	if out.FirstFraction <= 0 || out.FirstFraction >= 1 {
		out.FirstFraction = DefaultFirstFraction
	}
	if out.ThresholdRatio <= 0 {
		out.ThresholdRatio = DefaultThresholdRatio
	}
	if out.InclusionSampleSize <= 0 {
		out.InclusionSampleSize = DefaultInclusionSampleSize
	}
	return out
}

// Enabled reports whether the guard should capture and evaluate.
func (c Config) Enabled() bool {
	return c.Mode == ModeObserve || c.Mode == ModeEnforce
}

// Enforcing reports whether failing decisions should actually change the vote.
func (c Config) Enforcing() bool {
	return c.Mode == ModeEnforce
}
