package perf

import "time"

type Sample struct {
	ParticipantKey     string
	Model              string
	Responsive         bool
	FirstContent       time.Duration
	TimePerOutputToken time.Duration
}
