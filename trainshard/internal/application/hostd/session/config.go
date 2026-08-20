package session

import (
	"time"

	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Participant vo.Participant

	// Window is how long a signature stays fresh, which is also how long a request id is
	// remembered as served
	Window time.Duration
}
