package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

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
	_, err = journal.Diffs(1, 2)
	require.EqualError(t, err, "incomplete refusal diff range")
}

func TestExecutorUnreachableIgnoresAnHTTPAnswer(t *testing.T) {
	require.False(t, executorUnreachable(nil))
	require.False(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusInternalServerError}))
	require.False(t, executorUnreachable(&UpstreamStatusError{StatusCode: http.StatusNotFound}))
	require.True(t, executorUnreachable(net.ErrClosed))
	require.True(t, executorUnreachable(context.DeadlineExceeded))
	require.True(t, executorUnreachable(connect.NewError(connect.CodeUnavailable, errors.New("down"))))
	require.False(t, executorUnreachable(connect.NewError(connect.CodeInternal, errors.New("boom"))))
}
