package runtimeconfig

import (
	"context"
	"log/slog"
	"time"

	"common/nodemanager/gen"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SnapshotSource yields the current runtime-config snapshot for an epoch.
// Implementations must be safe for concurrent use.
type SnapshotSource interface {
	RuntimeConfigSnapshot(epochID uint64) Snapshot
}

// EpochSource returns the current chain epoch id. Optional; a nil EpochSource is
// treated as epoch 0.
type EpochSource interface {
	CurrentEpochID() uint64
}

// Notifier exposes a fan-out wake channel that is closed whenever the snapshot's
// params_block_height advances. NotifyChan returns ok=false when no notifier is
// configured (long-poll requests then fail with FailedPrecondition, matching the
// original dapi behavior).
type Notifier interface {
	NotifyChan() (<-chan struct{}, bool)
}

// ServerDeps configures a Server. Source is required; the rest are optional.
type ServerDeps struct {
	Source     SnapshotSource
	Epochs     EpochSource
	Notifier   Notifier
	MaxWaitCap func() time.Duration
	Log        *slog.Logger
}

// Server implements the GetRuntimeConfig long-poll loop independent of any
// transport or concrete config store.
type Server struct {
	source     SnapshotSource
	epochs     EpochSource
	notifier   Notifier
	maxWaitCap func() time.Duration
	log        *slog.Logger
}

// NewServer builds a Server. It panics if Source is nil.
func NewServer(d ServerDeps) *Server {
	if d.Source == nil {
		panic("runtimeconfig: ServerDeps.Source is required")
	}
	maxWaitCap := d.MaxWaitCap
	if maxWaitCap == nil {
		maxWaitCap = func() time.Duration { return DefaultMaxWaitCap }
	}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		source:     d.Source,
		epochs:     d.Epochs,
		notifier:   d.Notifier,
		maxWaitCap: maxWaitCap,
		log:        log,
	}
}

// Handle runs the long-poll loop for a single GetRuntimeConfig request.
//
// Semantics (preserved from the dapi implementation):
//   - full config when the client is behind, has never synced (height 0), or the
//     server has not synced params yet (ParamsBlockHeight 0);
//   - immediate "unchanged" when the client is caught up and max_wait <= 0;
//   - otherwise hold up to max_wait, waking early on a notifier signal, and
//     returning "unchanged" on timeout or a context/cancel error on ctx.Done.
func (s *Server) Handle(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
	maxWait := ClampMaxWait(req.GetMaxWaitSeconds(), s.maxWaitCap())
	clientHeight := req.GetClientParamsBlockHeight()

	for {
		var wake <-chan struct{}
		if maxWait > 0 {
			if s.notifier == nil {
				return nil, status.Error(codes.FailedPrecondition, "runtime config: notifier not configured")
			}
			// Subscribe before reading the snapshot to avoid lost wake-ups.
			ch, ok := s.notifier.NotifyChan()
			if !ok {
				return nil, status.Error(codes.FailedPrecondition, "runtime config: notifier not configured")
			}
			wake = ch
		}

		epochID := s.currentEpochID()
		snap := s.source.RuntimeConfigSnapshot(epochID)

		// Full config: initial fetch, server ahead, or server not synced yet.
		if clientHeight == 0 || snap.ParamsBlockHeight == 0 || snap.ParamsBlockHeight > clientHeight {
			s.log.Debug("runtime_config: returning full config",
				"clientParamsBlockHeight", clientHeight,
				"serverParamsBlockHeight", snap.ParamsBlockHeight,
				"epochID", epochID,
				"maxWait", maxWait,
				"devshardRequestsEnabled", snap.DevshardRequestsEnabled,
			)
			return &gen.GetRuntimeConfigResponse{
				Unchanged: false,
				Config:    snap.ToProto(),
			}, nil
		}

		// Client is caught up (server height > 0).
		if maxWait <= 0 {
			s.log.Debug("runtime_config: immediate unchanged",
				"clientParamsBlockHeight", clientHeight,
				"serverParamsBlockHeight", snap.ParamsBlockHeight,
				"epochID", epochID,
			)
			return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
		}

		s.log.Debug("runtime_config: long-poll waiting",
			"clientParamsBlockHeight", clientHeight,
			"serverParamsBlockHeight", snap.ParamsBlockHeight,
			"epochID", epochID,
			"maxWait", maxWait,
		)
		timer := time.NewTimer(maxWait)
		select {
		case <-wake:
			timer.Stop()
			// Re-evaluate the snapshot on the next loop iteration.
		case <-timer.C:
			return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
		case <-ctx.Done():
			timer.Stop()
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
}

func (s *Server) currentEpochID() uint64 {
	if s.epochs == nil {
		return 0
	}
	return s.epochs.CurrentEpochID()
}
