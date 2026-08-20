package node

import (
	"time"

	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Nodes            []vo.NodeRef
	Version          string
	SupportedVersion string
	MinFreeDiskBytes int64
	OptInTTL         time.Duration
	RefreshInterval  time.Duration
}
