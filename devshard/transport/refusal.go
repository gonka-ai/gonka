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
func (s *Server) verifyRefusedTimeout(ctx context.Context, st types.EscrowState, inferenceID uint64, rec *types.InferenceRecord, payload *host.InferencePayload, mempool []*types.DevshardTx, executor host.ExecutorClient, executorAddr string, nowUnix int64) (bool, error) {
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
	ctx = host.WithColdWalk(ctx, s.coldWalks, executorAddr)
	return host.VerifyRefusedProgress(ctx, st, inferenceID, rec, payload, mempool, executor, s.host, s.host, tip, journal, query, verify, nowUnix, executorUnreachable)
}

type refusalJournal struct {
	store    storage.Storage
	escrowID string
}

var _ host.RefusalWireReader = refusalJournal{}

func (j refusalJournal) Anchor(nonce uint64, root []byte) (bool, error) {
	hash, ok, err := j.store.DiffStateHash(j.escrowID, nonce)
	if err != nil || !ok {
		return false, err
	}
	return bytes.Equal(hash, root), nil
}

func (j refusalJournal) Pages(ctx context.Context, from, to uint64, next func() uint64, fn func([]types.DiffWire, bool) (func(context.Context) error, error)) error {
	if from > to {
		return nil
	}
	var pending []storage.DiffSize
	// capped is set when the read that filled pending hit its row limit, so
	// stored rows may follow the last one in hand.
	var capped bool
	for n := from; n <= to; {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A cursor jump leaves the leftover rows behind this nonce. A hole
		// leaves the next stored nonce ahead of it, which is reported below.
		if len(pending) == 0 || pending[0].Nonce < n {
			var err error
			pending, err = j.store.DiffSizes(j.escrowID, n, to, host.ChallengePageMaxNonces)
			if err != nil {
				return fmt.Errorf("get diff sizes: %w", err)
			}
			capped = len(pending) >= host.ChallengePageMaxNonces
		}
		if len(pending) == 0 {
			return &storage.DiffGapError{Expected: n, To: to}
		}
		if pending[0].Nonce != n {
			return &storage.DiffGapError{Expected: n, Next: pending[0].Nonce, To: to}
		}
		sizes := pending
		count := host.PackChallengePage(sizes)
		last := sizes[count-1].Nonce
		page, err := j.readRun(n, last, to)
		if err != nil {
			return err
		}
		more, err := j.pageHasMore(ctx, sizes, count, capped, last, to)
		if err != nil {
			return err
		}
		send, err := fn(page, more)
		page = nil
		if err != nil {
			return err
		}
		if send != nil {
			if err := send(ctx); err != nil {
				return err
			}
		}
		if !more {
			return nil
		}
		pending = pending[count:]
		n = last + 1
		if next != nil {
			if cur := next(); cur > n {
				n = cur
			}
		}
	}
	return nil
}

// Wires reads [from, to] again for a retried send. A hole is an error.
func (j refusalJournal) Wires(from, to uint64) ([]types.DiffWire, error) {
	if from > to {
		return nil, nil
	}
	return j.readRun(from, to, to)
}

// readRun reads [n, last] and requires every nonce in it. A gap error names to.
func (j refusalJournal) readRun(n, last, to uint64) ([]types.DiffWire, error) {
	wires, err := j.store.GetDiffWires(j.escrowID, n, last)
	if err != nil {
		return nil, fmt.Errorf("get diff wires: %w", err)
	}
	page := make([]types.DiffWire, 0, len(wires))
	expect := n
	for _, w := range wires {
		if w.Nonce != expect {
			next := uint64(0)
			if w.Nonce > expect {
				next = w.Nonce
			}
			return nil, &storage.DiffGapError{Expected: expect, Next: next, To: to}
		}
		page = append(page, w)
		if expect == last {
			break
		}
		expect++
	}
	if uint64(len(page)) != last-n+1 {
		return nil, &storage.DiffGapError{Expected: n + uint64(len(page)), To: to}
	}
	return page, nil
}

// pageHasMore reports whether another challenge page follows last. capped
// is whether the size read behind sizes hit its row limit; only then can a
// stored row follow the last one in sizes.
// A hole is returned without the caller delivering the page it already built.
func (j refusalJournal) pageHasMore(ctx context.Context, sizes []storage.DiffSize, count int, capped bool, last, to uint64) (bool, error) {
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
	if !capped {
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
// storage_busy is an answer as well, on HTTP 503 and on Connect
// Unavailable: the executor received the page and applied none of it.
// rpcRetry still retries that Unavailable for the non-inference budget;
// once the budget is spent the answer stays answered, so the payload is sent.
func executorUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if IsStorageBusy(err) {
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
