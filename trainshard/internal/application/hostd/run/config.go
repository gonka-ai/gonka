package run

import (
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Participant vo.Participant
	Nodes       []vo.NodeRef
	Limits      run.Limits
	Interval    time.Duration
	Patience    time.Duration
}
