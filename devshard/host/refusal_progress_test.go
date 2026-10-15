package host

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

var errAnswered = errors.New("http 409")

func progressUnreachable(err error) bool {
	return err != nil && !errors.Is(err, errAnswered)
}

type progressPeer struct {
	heads []struct {
		nonce uint64
		root  []byte
		err   error
	}
	headN int
	steps []struct {
		receipt []byte
		mempool []*types.DevshardTx
		err     error
	}
	stepN int
	pages [][]types.Diff
}

func (p *progressPeer) SessionHead(context.Context) (uint64, []byte, error) {
	idx := p.headN
	if idx >= len(p.heads) {
		idx = len(p.heads) - 1
	}
	p.headN++
	h := p.heads[idx]
	return h.nonce, h.root, h.err
}

func (p *progressPeer) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return nil, nil
}

func (p *progressPeer) ChallengeReceipt(_ context.Context, _ uint64, _ *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	p.pages = append(p.pages, append([]types.Diff(nil), diffs...))
	step := p.steps[p.stepN]
	if p.stepN+1 < len(p.steps) {
		p.stepN++
	}
	return step.receipt, step.mempool, step.err
}

type progressJournal struct {
	diffs []types.Diff
	fail  bool
}

func (j *progressJournal) Anchor(nonce uint64, root []byte) (bool, error) {
	if j.fail {
		return false, errors.New("storage unavailable")
	}
	for _, d := range j.diffs {
		if d.Nonce == nonce {
			return bytes.Equal(d.PostStateRoot, root), nil
		}
	}
	return false, nil
}

func (j *progressJournal) Diffs(from, to uint64) ([]types.Diff, error) {
	if j.fail {
		return nil, errors.New("storage unavailable")
	}
	if from > to {
		return nil, nil
	}
	var out []types.Diff
	for _, d := range j.diffs {
		if d.Nonce >= from && d.Nonce <= to {
			out = append(out, d)
		}
	}
	if uint64(len(out)) != to-from+1 {
		return nil, errors.New("incomplete refusal diff range")
	}
	return out, nil
}

func progressState(latest uint64) types.EscrowState {
	st := stateWithPendingFull(1, 1)
	st.LatestNonce = latest
	return st
}

func progressDiffs(n int, root []byte) []types.Diff {
	diffs := make([]types.Diff, n)
	for i := range diffs {
		diffs[i] = types.Diff{Nonce: uint64(i + 1), PostStateRoot: root}
	}
	return diffs
}

func runProgress(t *testing.T, st types.EscrowState, peer *progressPeer, journal *progressJournal, ev EvidenceVerifier) (bool, error) {
	t.Helper()
	return VerifyRefusedProgress(context.Background(), st, 1, testPayload(), nil, peer, nil, ev, peer, journal, time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable)
}

func TestRefusalRecheckRetriesAdvancedNonce(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	for _, match := range []bool{true, false} {
		name := "root mismatch"
		if match {
			name = "matching root"
		}
		t.Run(name, func(t *testing.T) {
			st := progressState(3)
			g := newEvidenceGroup(t)
			confirm, sig := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)
			secondRoot := root
			if !match {
				secondRoot = bytes.Repeat([]byte{9}, 32)
			}
			peer := &progressPeer{
				heads: []struct {
					nonce uint64
					root  []byte
					err   error
				}{
					{nonce: 1, root: root},
					{nonce: 2, root: secondRoot},
				},
				steps: []struct {
					receipt []byte
					mempool []*types.DevshardTx
					err     error
				}{
					{err: errAnswered},
					{receipt: sig, mempool: []*types.DevshardTx{confirm}},
				},
			}
			accept, err := runProgress(t, st, peer, &progressJournal{diffs: progressDiffs(3, root)}, g.sm)
			require.NoError(t, err)
			require.Equal(t, 2, peer.headN)
			if match {
				require.False(t, accept)
				require.Equal(t, []int{2, 1}, pageLens(peer.pages))
			} else {
				require.True(t, accept)
				require.Equal(t, []int{2}, pageLens(peer.pages))
			}
		})
	}
}

func TestRefusalProgressRetryCap(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{
			{nonce: 1, root: root},
			{nonce: 2, root: root},
			{nonce: 3, root: root},
			{nonce: 4, root: root},
			{nonce: 5, root: root},
		},
		steps: []struct {
			receipt []byte
			mempool []*types.DevshardTx
			err     error
		}{{err: errAnswered}},
	}
	accept, err := runProgress(t, progressState(8), peer, &progressJournal{diffs: progressDiffs(8, root)}, newEvidenceGroup(t).sm)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, 1+RefusalProgressRetries, peer.headN)
	require.Len(t, peer.pages, 1+RefusalProgressRetries)
}

func TestRefusalUnreachableSkipsFullDiffs(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{err: net.ErrClosed}},
	}
	journal := &progressJournal{diffs: progressDiffs(2, root)}
	accept, err := runProgress(t, progressState(2), peer, journal, newEvidenceGroup(t).sm)
	require.NoError(t, err)
	require.True(t, accept)
	require.Empty(t, peer.pages)
}

func TestRefusalUnverifiedReceiptDoesNotReject(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{nonce: 2, root: root}},
		steps: []struct {
			receipt []byte
			mempool []*types.DevshardTx
			err     error
		}{{receipt: []byte("receipt")}},
	}
	accept, err := runProgress(t, progressState(2), peer, &progressJournal{diffs: progressDiffs(2, root)}, newEvidenceGroup(t).sm)
	require.NoError(t, err)
	require.True(t, accept, "a receipt that does not verify must not reject the timeout")
	require.Equal(t, []int{0}, pageLens(peer.pages))
}

func TestRefusalFullHistoryWhenTipDoesNotMatch(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	st := progressState(2)
	g := newEvidenceGroup(t)
	confirm, sig := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{nonce: 1, root: bytes.Repeat([]byte{2}, 32)}},
		steps: []struct {
			receipt []byte
			mempool []*types.DevshardTx
			err     error
		}{{receipt: sig, mempool: []*types.DevshardTx{confirm}}},
	}
	accept, err := runProgress(t, st, peer, &progressJournal{diffs: progressDiffs(2, root)}, g.sm)
	require.NoError(t, err)
	require.False(t, accept)
	require.Equal(t, []int{2}, pageLens(peer.pages))
}

func TestRefusalStorageFailure(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{nonce: 1, root: root}},
	}
	accept, err := runProgress(t, progressState(2), peer, &progressJournal{fail: true}, newEvidenceGroup(t).sm)
	require.Error(t, err)
	require.False(t, accept)
	require.Empty(t, peer.pages)
}

func pageLens(pages [][]types.Diff) []int {
	out := make([]int, len(pages))
	for i, page := range pages {
		out[i] = len(page)
	}
	return out
}
