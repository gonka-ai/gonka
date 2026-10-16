package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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

// verifyRefusedTimeout fetches the executor tip and challenges with the diffs
// after that nonce, one page at a time. The verify budget stays at
// RefusalVerifyBudget even when the peer client allows a longer vote.
func (s *Server) verifyRefusedTimeout(ctx context.Context, st types.EscrowState, inferenceID uint64, rec *types.InferenceRecord, payload *host.InferencePayload, mempool []*types.DevshardTx, executor host.ExecutorClient, nowUnix int64) (bool, error) {
	var tip host.SessionTip
	if sh, ok := executor.(host.SessionTip); ok {
		tip = sh
	}
	query, verify := 30*time.Second, host.RefusalVerifyBudget
	if budgets, ok := executor.(refusalBudgetClient); ok {
		if d := budgets.RefusalQueryBudget(); d > 0 && d < query {
			query = d
		}
		if d := budgets.RefusalVerifyBudget(); d > 0 && d < verify {
			verify = d
		}
	}
	var journal host.RefusalJournal
	if s.store != nil {
		journal = refusalJournal{store: s.store, escrowID: s.host.EscrowID()}
	}
	return host.VerifyRefusedProgress(ctx, st, inferenceID, rec, payload, mempool, executor, s.host, s.host, tip, journal, query, verify, nowUnix, executorUnreachable)
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

func (j refusalJournal) Pages(ctx context.Context, from, to uint64, fn func([]types.Diff, bool) error) error {
	if from > to {
		return nil
	}
	for n := from; n <= to; {
		if err := ctx.Err(); err != nil {
			return err
		}
		sizes, err := j.store.DiffSizes(j.escrowID, n, to, host.ChallengePageMaxNonces)
		if err != nil {
			return fmt.Errorf("get diff sizes: %w", err)
		}
		if len(sizes) == 0 {
			return &storage.DiffGapError{Expected: n, To: to}
		}
		if sizes[0].Nonce != n {
			return &storage.DiffGapError{Expected: n, Next: sizes[0].Nonce, To: to}
		}
		count := host.PackChallengePage(sizes)
		last := sizes[count-1].Nonce
		recs, err := j.store.GetDiffs(j.escrowID, n, last)
		if err != nil {
			return fmt.Errorf("get diffs: %w", err)
		}
		page := make([]types.Diff, 0, len(recs))
		expect := n
		var gap *storage.DiffGapError
		for _, rec := range recs {
			if rec.Nonce != expect {
				next := uint64(0)
				if rec.Nonce > expect {
					next = rec.Nonce
				}
				gap = &storage.DiffGapError{Expected: expect, Next: next, To: to}
				break
			}
			page = append(page, rec.Diff)
			if expect == last {
				break
			}
			expect++
		}
		if gap == nil && uint64(len(page)) != last-n+1 {
			gap = &storage.DiffGapError{Expected: n + uint64(len(page)), To: to}
		}
		if gap != nil {
			return gap
		}
		more, err := j.pageHasMore(ctx, sizes, count, last, to)
		if err != nil {
			return err
		}
		if err := fn(page, more); err != nil {
			return err
		}
		page = nil
		if !more {
			return nil
		}
		n = last + 1
	}
	return nil
}

// pageHasMore reports whether another challenge page follows last.
// A hole is returned without the caller delivering the page it already built.
func (j refusalJournal) pageHasMore(ctx context.Context, sizes []storage.DiffSize, count int, last, to uint64) (bool, error) {
	if last == to {
		return false, nil
	}
	if count < len(sizes) {
		next := sizes[count].Nonce
		if next != last+1 {
			return false, &storage.DiffGapError{Expected: last + 1, Next: next, To: to}
		}
		return true, nil
	}
	if len(sizes) < host.ChallengePageMaxNonces {
		return false, &storage.DiffGapError{Expected: last + 1, To: to}
	}
	peek, err := j.store.DiffSizes(j.escrowID, last+1, to, 1)
	if err != nil {
		return false, fmt.Errorf("get diff sizes: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(peek) == 0 {
		return false, &storage.DiffGapError{Expected: last + 1, To: to}
	}
	if peek[0].Nonce != last+1 {
		return false, &storage.DiffGapError{Expected: last + 1, Next: peek[0].Nonce, To: to}
	}
	return true, nil
}

// executorUnreachable reports that a refusal probe got no usable response.
// A missing peer session and a 429 are that case: the executor did not
// answer the probe, so the timeout is accepted without resending the
// journal. Any other HTTP status, including 404 and 500, means the
// executor answered. A message-size ResourceExhausted is an answer too.
func executorUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPeerNotReady) {
		return true
	}
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		return status.StatusCode == http.StatusTooManyRequests
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		switch connectErr.Code() {
		case connect.CodeCanceled, connect.CodeDeadlineExceeded, connect.CodeUnavailable, connect.CodeUnknown:
			return true
		case connect.CodeResourceExhausted:
			return !isConnectMessageTooLarge(err)
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
