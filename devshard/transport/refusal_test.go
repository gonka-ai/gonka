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
	err = journal.Pages(context.Background(), 1, 2, func([]types.DiffWire, bool) (func(context.Context) error, error) {
		return nil, nil
	})
	var gap *storage.DiffGapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, uint64(2), gap.Expected)
}

func TestExecutorUnreachableIgnoresAnHTTPAnswer(t *testing.T) {
	require.False(t, executorUnreachable(nil))
	require.False(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusInternalServerError}))
	require.False(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusNotFound}))
	require.True(t, executorUnreachable(net.ErrClosed))
	require.True(t, executorUnreachable(context.DeadlineExceeded))
	require.True(t, executorUnreachable(connect.NewError(connect.CodeUnavailable, errors.New("down"))))
	require.False(t, executorUnreachable(connect.NewError(connect.CodeInternal, errors.New("boom"))))
	require.True(t, executorUnreachable(ErrPeerNotReady))
	require.True(t, executorUnreachable(fmt.Errorf("get payload: %w", ErrPeerPayloadUnanswered)))
	require.True(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusTooManyRequests}))
	require.True(t, executorUnreachable(connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded"))))
	require.False(t, executorUnreachable(connect.NewError(connect.CodeResourceExhausted, errors.New("message size 99 is larger than configured max 10"))))
}
