package perf

import "time"

type Sample struct {
	ParticipantKey     string
	Model              string
	Responsive         bool
	FirstContent       time.Duration
	TimePerOutputToken time.Duration
	LaunchedAt         time.Time
}

func (s Sample) launchedBefore(instant time.Time) bool {
	return !s.LaunchedAt.IsZero() && s.LaunchedAt.Before(instant)
}
