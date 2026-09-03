package localstate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

const sessionDir = "sessions"

func (s *Store) Sessions() run.SessionLog { return sessions{store: s} }

type sessions struct {
	store *Store
}

func (s sessions) Record(_ context.Context, shardID vo.ShardID, node vo.NodeRef, at time.Time) (io.WriteCloser, error) {
	dir := filepath.Join(s.store.dir, sessionDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	name := fmt.Sprintf("%s_%s_%s.log", shardID, node.NodeID, at.UTC().Format("20060102T150405.000"))
	return os.OpenFile(filepath.Join(dir, filepath.Base(name)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
