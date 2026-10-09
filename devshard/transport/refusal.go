package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"devshard/host"
	"devshard/storage"
	"devshard/types"
)

type refusalBudgetClient interface {
	RefusalQueryBudget() time.Duration
	RefusalVerifyBudget() time.Duration
}

// verifyRefusedTimeout fetches the executor tip, checks it against the stored
// state hash, and challenges with the diffs after that nonce. A tip the
// verifier cannot check falls back to the diffs from nonce 1.
func (s *Server) verifyRefusedTimeout(ctx context.Context, st types.EscrowState, inferenceID uint64, payload *host.InferencePayload, mempool []*types.DevshardTx, executor host.ExecutorClient, nowUnix int64) (bool, error) {
	var tip host.SessionTip
	if sh, ok := executor.(host.SessionTip); ok {
		tip = sh
	}
	query, verify := 30*time.Second, 3*time.Minute
	if budgets, ok := executor.(refusalBudgetClient); ok {
		if d := budgets.RefusalQueryBudget(); d > 0 {
			query = d
		}
		if d := budgets.RefusalVerifyBudget(); d > 0 {
			verify = d
		}
	}
	var journal host.RefusalJournal
	if s.store != nil {
		journal = refusalJournal{store: s.store, escrowID: s.host.EscrowID()}
	}
	return host.VerifyRefusedProgress(ctx, st, inferenceID, payload, mempool, executor, s.host, s.host, tip, journal, query, verify, nowUnix, executorUnreachable)
}

type refusalJournal struct {
	store    storage.Storage
	escrowID string
}

func (j refusalJournal) Anchor(nonce uint64, root []byte) (bool, error) {
	recs, err := j.store.GetDiffs(j.escrowID, nonce, nonce)
	if err != nil {
		return false, err
	}
	if len(recs) != 1 || recs[0].Diff.Nonce != nonce {
		return false, nil
	}
	return bytes.Equal(recs[0].StateHash, root), nil
}

func (j refusalJournal) Diffs(from, to uint64) ([]types.Diff, error) {
	if from > to {
		return nil, nil
	}
	diffs := make([]types.Diff, 0, to-from+1)
	expect := from
	for start := from; start <= to; {
		end := start + uint64(storage.DiffPageMaxNonces) - 1
		if end < start || end > to {
			end = to
		}
		recs, err := j.store.GetDiffs(j.escrowID, start, end)
		if err != nil {
			return nil, err
		}
		if uint64(len(recs)) != end-start+1 {
			return nil, fmt.Errorf("incomplete refusal diff range")
		}
		for _, rec := range recs {
			if rec.Diff.Nonce != expect {
				return nil, fmt.Errorf("noncontiguous refusal diffs")
			}
			diffs = append(diffs, rec.Diff)
			expect++
		}
		if end == to {
			break
		}
		start = end + 1
	}
	return diffs, nil
}

// executorUnreachable reports that a refusal probe got no HTTP response.
// An HTTP status, including 404 and 500, means the executor answered.
func executorUnreachable(err error) bool {
	if err == nil {
		return false
	}
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		return false
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		switch connectErr.Code() {
		case connect.CodeCanceled, connect.CodeDeadlineExceeded, connect.CodeUnavailable, connect.CodeUnknown:
			return true
		default:
			return false
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
