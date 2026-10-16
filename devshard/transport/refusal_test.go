package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/storage"
	"devshard/types"
)

type refusalMapStore struct {
	storage.Storage
	recs []types.DiffRecord
}

func (m refusalMapStore) GetDiffs(_ string, from, to uint64) ([]types.DiffRecord, error) {
	var out []types.DiffRecord
	for _, rec := range m.recs {
		if rec.Diff.Nonce >= from && rec.Diff.Nonce <= to {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (m refusalMapStore) GetDiffWires(_ string, from, to uint64) ([]types.DiffWire, error) {
	var out []types.DiffWire
	for _, rec := range m.recs {
		if rec.Nonce < from || rec.Nonce > to {
			continue
		}
		txs, err := proto.Marshal(&types.DiffContent{Txs: rec.Txs})
		if err != nil {
			return nil, err
		}
		out = append(out, types.DiffWire{
			Nonce:         rec.Nonce,
			Txs:           txs,
			UserSig:       rec.UserSig,
			PostStateRoot: rec.PostStateRoot,
		})
	}
	return out, nil
}

func (m refusalMapStore) DiffSizes(_ string, from, to uint64, limit int) ([]storage.DiffSize, error) {
	var out []storage.DiffSize
	for _, rec := range m.recs {
		if rec.Nonce < from || rec.Nonce > to {
			continue
		}
		out = append(out, storage.DiffSize{Nonce: rec.Nonce, Bytes: 1})
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func TestRefusalJournalRequiresAContiguousRange(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := refusalJournal{store: refusalMapStore{recs: []types.DiffRecord{
		{Diff: types.Diff{Nonce: 1, PostStateRoot: append([]byte(nil), root...)}, StateHash: append([]byte(nil), root...)},
		{Diff: types.Diff{Nonce: 3}, StateHash: append([]byte(nil), root...)},
	}}, escrowID: "escrow-1"}
	match, err := journal.Anchor(1, root)
	require.NoError(t, err)
	require.True(t, match)
	match, err = journal.Anchor(1, bytes.Repeat([]byte{2}, 32))
	require.NoError(t, err)
	require.False(t, match)
	err = journal.Pages(context.Background(), 1, 2, nil, func([]types.DiffWire, bool) (func(context.Context) error, error) {
		return nil, nil
	})
	var gap *storage.DiffGapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, uint64(2), gap.Expected)
}

// cursorSkipStore reports each diff as its own page and records every read.
type cursorSkipStore struct {
	refusalMapStore
	sizeFrom  []uint64
	wireSpans [][2]uint64
}

func (s *cursorSkipStore) DiffSizes(escrowID string, from, to uint64, limit int) ([]storage.DiffSize, error) {
	s.sizeFrom = append(s.sizeFrom, from)
	sizes, err := s.refusalMapStore.DiffSizes(escrowID, from, to, limit)
	for i := range sizes {
		sizes[i].Bytes = host.ChallengePageMaxBytes
	}
	return sizes, err
}

func (s *cursorSkipStore) GetDiffWires(escrowID string, from, to uint64) ([]types.DiffWire, error) {
	s.wireSpans = append(s.wireSpans, [2]uint64{from, to})
	return s.refusalMapStore.GetDiffWires(escrowID, from, to)
}

func TestRefusalJournalSkipsNoncesTheCursorPassed(t *testing.T) {
	recs := make([]types.DiffRecord, 3)
	for i := range recs {
		recs[i] = types.DiffRecord{Diff: types.Diff{Nonce: uint64(i + 1)}}
	}
	store := &cursorSkipStore{refusalMapStore: refusalMapStore{recs: recs}}
	journal := refusalJournal{store: store, escrowID: "escrow-1"}

	cursor := uint64(1)
	var seen []uint64
	err := journal.Pages(context.Background(), 1, 3, func() uint64 { return cursor }, func(page []types.DiffWire, more bool) (func(context.Context) error, error) {
		require.Len(t, page, 1)
		seen = append(seen, page[0].Nonce)
		require.True(t, more || page[0].Nonce == 3)
		return func(context.Context) error {
			cursor = 4
			return nil
		}, nil
	})
	require.NoError(t, err)
	require.Equal(t, []uint64{1}, seen)
	require.Equal(t, []uint64{1}, store.sizeFrom)
	require.Equal(t, [][2]uint64{{1, 1}}, store.wireSpans)

	store.sizeFrom, store.wireSpans, seen = nil, nil, nil
	cursor = 1
	err = journal.Pages(context.Background(), 1, 3, func() uint64 { return cursor }, func(page []types.DiffWire, _ bool) (func(context.Context) error, error) {
		require.Len(t, page, 1)
		seen = append(seen, page[0].Nonce)
		return func(context.Context) error {
			if cursor == 1 {
				cursor = 3
			}
			return nil
		}, nil
	})
	require.NoError(t, err)
	require.Equal(t, []uint64{1, 3}, seen)
	require.Equal(t, []uint64{1, 3}, store.sizeFrom)
	require.Equal(t, [][2]uint64{{1, 1}, {3, 3}}, store.wireSpans)
}

func TestExecutorUnreachableIgnoresAnHTTPAnswer(t *testing.T) {
	require.False(t, executorUnreachable(nil))
	require.False(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusInternalServerError}))
	require.False(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusNotFound}))
	require.True(t, executorUnreachable(net.ErrClosed))
	require.True(t, executorUnreachable(context.DeadlineExceeded))
	require.True(t, executorUnreachable(connect.NewError(connect.CodeUnavailable, errors.New("down"))))
	require.False(t, executorUnreachable(connect.NewError(connect.CodeInternal, errors.New("boom"))))
	busy := connect.NewError(connect.CodeUnavailable, errors.New("persist diff: storage busy"))
	busy.Meta().Set(HeaderDevshardError, DevshardErrorStorageBusy)
	wrapped := fmt.Errorf("challenge-receipt: %w", busy)
	require.True(t, IsStorageBusy(wrapped))
	require.False(t, executorUnreachable(wrapped))
	require.True(t, IsRetryableNonInference(wrapped))
	require.False(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusServiceUnavailable, DevshardError: DevshardErrorStorageBusy}))
	require.True(t, executorUnreachable(ErrPeerNotReady))
	require.True(t, executorUnreachable(fmt.Errorf("get payload: %w", ErrPeerPayloadUnanswered)))
	require.True(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusTooManyRequests}))
	require.True(t, executorUnreachable(connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded"))))
	require.False(t, executorUnreachable(connect.NewError(connect.CodeResourceExhausted, errors.New("message size 99 is larger than configured max 10"))))
}
