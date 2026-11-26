package event_listener

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

const hostEventDBFile = "host_events.sqlite"

// HostEventStoreConfig opens the feed store for one participant.
type HostEventStoreConfig struct {
	Writer  string
	DataDir string
}

// NewHostEventStore opens the feed store for one participant.
// DAPI_HOST_EVENTS_STORE selects sqlite or postgres. An empty value is sqlite,
// at DataDir/host_events.sqlite. Startup fails when the selected backend
// cannot be opened. Stats-storage flags do not select this store.
func NewHostEventStore(ctx context.Context, cfg HostEventStoreConfig) (HostEventStore, error) {
	if cfg.Writer == "" {
		return nil, fmt.Errorf("host event store writer is empty")
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("host event store data dir is empty")
	}
	backend, err := hostEventBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "postgres":
		logging.Info("Using PostgreSQL host event store", types.System, "writer", cfg.Writer)
		return openPostgresStore(ctx, cfg.Writer)
	case "sqlite":
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			return nil, err
		}
		path := filepath.Join(cfg.DataDir, hostEventDBFile)
		logging.Info("Using SQLite host event store", types.System, "path", path, "writer", cfg.Writer)
		return openSQLiteStore(path, cfg.Writer)
	default:
		return nil, fmt.Errorf("unknown host event store backend %q", backend)
	}
}

func hostEventBackend() (string, error) {
	switch strings.TrimSpace(os.Getenv("DAPI_HOST_EVENTS_STORE")) {
	case "", "sqlite":
		return "sqlite", nil
	case "postgres":
		return "postgres", nil
	default:
		return "", fmt.Errorf("DAPI_HOST_EVENTS_STORE must be sqlite or postgres")
	}
}
