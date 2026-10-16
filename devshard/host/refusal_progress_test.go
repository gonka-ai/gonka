package host

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

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

// Challenge page the walk has to keep. These are the planned
// ChallengePageMaxNonces and ChallengePageMaxBytes; they are not production
// constants yet. A single diff is one page even when it is larger than the
// byte budget.
const (
	refusalPageMaxNonces = 8192
	refusalPageMaxBytes  = 4 << 20
)

// onePageJournal is a RefusalJournal that fails the test when one Diffs call
// spans more than one challenge page.
type onePageJournal struct {
	t     *testing.T
	diffs []types.Diff
	calls []struct{ from, to uint64 }
}

func (j *onePageJournal) Anchor(nonce uint64, root []byte) (bool, error) {
	for _, d := range j.diffs {
		if d.Nonce == nonce {
			return bytes.Equal(d.PostStateRoot, root), nil
		}
	}
	return false, nil
}

func (j *onePageJournal) Diffs(from, to uint64) ([]types.Diff, error) {
	if from > to {
		return nil, nil
	}
	var out []types.Diff
	var stored int
	for _, d := range j.diffs {
		if d.Nonce >= from && d.Nonce <= to {
			out = append(out, d)
			stored += proto.Size(&types.DiffContent{Txs: d.Txs})
		}
	}
	if uint64(len(out)) != to-from+1 {
		return nil, errors.New("incomplete refusal diff range")
	}
	if len(out) > 1 && (len(out) > refusalPageMaxNonces || stored > refusalPageMaxBytes) {
		j.t.Fatalf("RefusalJournal.Diffs(%d, %d) is %d diffs and %d bytes; a challenge page is at most %d nonces or %d bytes",
			from, to, len(out), stored, refusalPageMaxNonces, refusalPageMaxBytes)
	}
	j.calls = append(j.calls, struct{ from, to uint64 }{from, to})
	return out, nil
}

func diffWithStoredBytes(nonce uint64, root []byte, want int) types.Diff {
	model := strings.Repeat("m", want)
	d := types.Diff{Nonce: nonce, PostStateRoot: append([]byte(nil), root...)}
	for range 8 {
		d.Txs = []*types.DevshardTx{{
			Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{Model: model}},
		}}
		got := proto.Size(&types.DiffContent{Txs: d.Txs})
		if got >= want && got < want+8 {
			return d
		}
		delta := want - got
		if delta >= 0 {
			model += strings.Repeat("m", delta)
			continue
		}
		if len(model) <= -delta {
			model = "m"
			continue
		}
		model = model[:len(model)+delta]
	}
	return d
}

// TestRefusalJournalOnePageAtATime pins the page the refused-timeout walk
// may read. Today VerifyRefusedProgress asks for the whole suffix in one
// Diffs call, so both cases fail.
func TestRefusalJournalOnePageAtATime(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	run := func(t *testing.T, st types.EscrowState, peer *progressPeer, journal *onePageJournal) (bool, error) {
		t.Helper()
		return VerifyRefusedProgress(
			context.Background(), st, 1, testPayload(), nil,
			peer, nil, newEvidenceGroup(t).sm, peer, journal,
			time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable,
		)
	}
	t.Run("nonce cap", func(t *testing.T) {
		latest := uint64(refusalPageMaxNonces + 2)
		st := progressState(latest)
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{nonce: 1, root: root}},
			steps: []struct {
				receipt []byte
				mempool []*types.DevshardTx
				err     error
			}{{err: errAnswered}},
		}
		journal := &onePageJournal{t: t, diffs: progressDiffs(int(latest), root)}
		accept, err := run(t, st, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		require.NotEmpty(t, journal.calls)
	})
	t.Run("byte cap", func(t *testing.T) {
		half := refusalPageMaxBytes/2 + 1024
		st := progressState(2)
		diffs := []types.Diff{
			diffWithStoredBytes(1, root, half),
			diffWithStoredBytes(2, root, half),
		}
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{err: errAnswered}},
			steps: []struct {
				receipt []byte
				mempool []*types.DevshardTx
				err     error
			}{{err: errAnswered}},
		}
		journal := &onePageJournal{t: t, diffs: diffs}
		accept, err := run(t, st, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		require.NotEmpty(t, journal.calls)
	})
}
