package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/storage"
	"devshard/types"
)

var errAnswered = errors.New("http 409")

func progressUnreachable(err error) bool {
	return err != nil && !errors.Is(err, errAnswered) && !sessionNotFound(err)
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
	stepN           int
	pages           [][]types.Diff
	sawPayload      []bool
	versions        []string
	decodedReceipts int
	payloadOnWires  int
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

func (p *progressPeer) takeStep(payload bool, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	p.pages = append(p.pages, append([]types.Diff(nil), diffs...))
	p.sawPayload = append(p.sawPayload, payload)
	step := p.steps[p.stepN]
	if p.stepN+1 < len(p.steps) {
		p.stepN++
	}
	return step.receipt, step.mempool, step.err
}

func (p *progressPeer) ChallengeCatchUp(_ context.Context, _ uint64, diffs []types.DiffWire, version string) error {
	p.versions = append(p.versions, version)
	page := make([]types.Diff, len(diffs))
	for i, d := range diffs {
		page[i] = types.Diff{Nonce: d.Nonce, PostStateRoot: d.PostStateRoot}
	}
	_, _, err := p.takeStep(false, page)
	return err
}

func (p *progressPeer) ChallengeReceipt(_ context.Context, _ uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	if len(diffs) > 0 {
		p.decodedReceipts++
	}
	return p.takeStep(payload != nil, diffs)
}

func (p *progressPeer) ChallengeReceiptWires(_ context.Context, _ uint64, payload *InferencePayload, diffs []types.DiffWire) ([]byte, []*types.DevshardTx, error) {
	if payload != nil && len(diffs) > 0 {
		p.payloadOnWires++
	}
	page := make([]types.Diff, len(diffs))
	for i, d := range diffs {
		page[i] = types.Diff{Nonce: d.Nonce, PostStateRoot: append([]byte(nil), d.PostStateRoot...)}
	}
	return p.takeStep(payload != nil, page)
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

func (j *progressJournal) Pages(ctx context.Context, from, to uint64, _ func() uint64, fn func([]types.DiffWire, bool) (func(context.Context) error, error)) error {
	if j.fail {
		return errors.New("storage unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if from > to {
		return nil
	}
	var out []types.Diff
	for _, d := range j.diffs {
		if d.Nonce >= from && d.Nonce <= to {
			out = append(out, d)
		}
	}
	if uint64(len(out)) != to-from+1 {
		return errors.New("incomplete refusal diff range")
	}
	if len(out) == 0 {
		return nil
	}
	wires, err := journalWires(out)
	if err != nil {
		return err
	}
	send, err := fn(wires, false)
	wires = nil
	if err != nil {
		return err
	}
	if send != nil {
		return send(ctx)
	}
	return nil
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
	return VerifyRefusedProgress(context.Background(), st, 1, st.Inferences[1], testPayload(), nil, peer, nil, ev, peer, journal, time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable)
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
	require.Equal(t, 1, peer.headN, "a root mismatch is not retried")
	require.Equal(t, []int{1}, pageLens(peer.pages))
	require.Equal(t, uint64(2), peer.pages[0][0].Nonce)
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

// onePageJournal yields [from, to] one challenge page at a time and fails the
// test if a page it hands the walk is over the budget.
type onePageJournal struct {
	t     *testing.T
	diffs []types.Diff
	calls int
}

func (j *onePageJournal) Anchor(nonce uint64, root []byte) (bool, error) {
	for _, d := range j.diffs {
		if d.Nonce == nonce {
			return bytes.Equal(d.PostStateRoot, root), nil
		}
	}
	return false, nil
}

func (j *onePageJournal) Pages(ctx context.Context, from, to uint64, _ func() uint64, fn func([]types.DiffWire, bool) (func(context.Context) error, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if from > to {
		return nil
	}
	var page []types.Diff
	stored := 0
	flush := func(more bool) error {
		if len(page) == 0 {
			return nil
		}
		if len(page) > 1 && (len(page) > ChallengePageMaxNonces || stored > ChallengePageMaxBytes) {
			j.t.Fatalf("challenge page is %d diffs and %d bytes; the budget is %d nonces or %d bytes",
				len(page), stored, ChallengePageMaxNonces, ChallengePageMaxBytes)
		}
		j.calls++
		wires, err := journalWires(page)
		if err != nil {
			return err
		}
		send, err := fn(wires, more)
		page = nil
		wires = nil
		stored = 0
		if err != nil {
			return err
		}
		if send != nil {
			return send(ctx)
		}
		return nil
	}
	for _, d := range j.diffs {
		if d.Nonce < from || d.Nonce > to {
			continue
		}
		cost := proto.Size(&types.DiffContent{Txs: d.Txs}) + ChallengeDiffWireOverhead
		if len(page) > 0 && (len(page) == ChallengePageMaxNonces || stored+cost > ChallengePageMaxBytes) {
			if err := flush(true); err != nil {
				return err
			}
		}
		page = append(page, d)
		stored += cost
	}
	return flush(false)
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

func answeredStep() []struct {
	receipt []byte
	mempool []*types.DevshardTx
	err     error
} {
	return []struct {
		receipt []byte
		mempool []*types.DevshardTx
		err     error
	}{{}}
}

func runOnePage(t *testing.T, st types.EscrowState, peer *progressPeer, journal RefusalJournal) (bool, error) {
	t.Helper()
	return VerifyRefusedProgress(
		context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
		peer, nil, newEvidenceGroup(t).sm, peer, journal,
		time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable,
	)
}

func assertPagedSends(t *testing.T, pages [][]types.Diff) {
	t.Helper()
	require.NotEmpty(t, pages)
	for _, page := range pages {
		if len(page) <= 1 {
			continue
		}
		if len(page) > ChallengePageMaxNonces || challengePageCost(page) > ChallengePageMaxBytes {
			t.Fatalf("ChallengeReceipt carried %d diffs and %d bytes", len(page), challengePageCost(page))
		}
	}
}

func sentNonces(pages [][]types.Diff) []uint64 {
	var out []uint64
	for _, page := range pages {
		for _, d := range page {
			out = append(out, d.Nonce)
		}
	}
	return out
}

// TestRefusalJournalOnePageAtATime pins the page the refused-timeout walk sends.
func TestRefusalJournalOnePageAtATime(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	t.Run("nonce cap", func(t *testing.T) {
		latest := uint64(ChallengePageMaxNonces + 2)
		st := progressState(latest)
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{nonce: 1, root: root}},
			steps: answeredStep(),
		}
		journal := &onePageJournal{t: t, diffs: progressDiffs(int(latest), root)}
		accept, err := runOnePage(t, st, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		assertPagedSends(t, peer.pages)
		want := make([]uint64, latest-1)
		for i := range want {
			want[i] = uint64(i + 2)
		}
		require.Equal(t, want, sentNonces(peer.pages))
	})
	t.Run("byte cap", func(t *testing.T) {
		half := ChallengePageMaxBytes/2 + 1024
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
			}{{err: errors.New("session not found")}},
			steps: answeredStep(),
		}
		journal := &onePageJournal{t: t, diffs: diffs}
		accept, err := runOnePage(t, st, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		assertPagedSends(t, peer.pages)
		require.Equal(t, []uint64{1, 2}, sentNonces(peer.pages))
		require.Greater(t, len(peer.pages), 1)
	})
}

func TestRefusalCursorChoosesTheSuffix(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(4, root)}
	st := progressState(4)

	t.Run("ahead of verifier", func(t *testing.T) {
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{nonce: st.LatestNonce + 5, root: root}},
			steps: answeredStep(),
		}
		accept, err := runProgress(t, st, peer, journal, newEvidenceGroup(t).sm)
		require.NoError(t, err)
		require.True(t, accept)
		require.Equal(t, 1, peer.headN)
		require.Equal(t, []int{0}, pageLens(peer.pages))
		require.Equal(t, []bool{true}, peer.sawPayload)
	})

	t.Run("storage busy", func(t *testing.T) {
		busy := errors.New("storage busy")
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{err: busy}},
			steps: answeredStep(),
		}
		accept, err := VerifyRefusedProgress(
			context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, newEvidenceGroup(t).sm, peer, journal,
			time.Second, 2*time.Second, deadlinePassedRefused(st, 1),
			func(err error) bool { return err != nil && !errors.Is(err, busy) },
		)
		require.NoError(t, err)
		require.True(t, accept)
		require.Equal(t, []int{0}, pageLens(peer.pages))
		require.Empty(t, sentNonces(peer.pages))
	})

	t.Run("session not found", func(t *testing.T) {
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{err: errors.New("session not found")}},
			steps: answeredStep(),
		}
		accept, err := runProgress(t, st, peer, journal, newEvidenceGroup(t).sm)
		require.NoError(t, err)
		require.True(t, accept)
		require.Equal(t, []uint64{1, 2, 3, 4}, sentNonces(peer.pages))
	})

	t.Run("suffix carries the payload", func(t *testing.T) {
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{nonce: 1, root: root}},
			steps: answeredStep(),
		}
		accept, err := runProgress(t, st, peer, journal, newEvidenceGroup(t).sm)
		require.NoError(t, err)
		require.True(t, accept)
		require.Equal(t, []int{3}, pageLens(peer.pages))
		require.Equal(t, []bool{true}, peer.sawPayload)
		require.Equal(t, []uint64{2, 3, 4}, sentNonces(peer.pages))
		require.Equal(t, 1, peer.payloadOnWires)
		require.Zero(t, peer.decodedReceipts)
	})
}

type scriptedPeer struct {
	progressPeer
	cancel context.CancelFunc
	block  bool
}

func (p *scriptedPeer) interrupt(ctx context.Context) error {
	if p.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	return nil
}

func (p *scriptedPeer) ChallengeCatchUp(ctx context.Context, id uint64, diffs []types.DiffWire, version string) error {
	if err := p.interrupt(ctx); err != nil {
		return err
	}
	return p.progressPeer.ChallengeCatchUp(ctx, id, diffs, version)
}

func (p *scriptedPeer) ChallengeReceipt(ctx context.Context, id uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	if err := p.interrupt(ctx); err != nil {
		return nil, nil, err
	}
	return p.progressPeer.ChallengeReceipt(ctx, id, payload, diffs)
}

func (p *scriptedPeer) ChallengeReceiptWires(ctx context.Context, id uint64, payload *InferencePayload, diffs []types.DiffWire) ([]byte, []*types.DevshardTx, error) {
	if err := p.interrupt(ctx); err != nil {
		return nil, nil, err
	}
	return p.progressPeer.ChallengeReceiptWires(ctx, id, payload, diffs)
}

func journalWires(diffs []types.Diff) ([]types.DiffWire, error) {
	out := make([]types.DiffWire, len(diffs))
	for i, d := range diffs {
		txs, err := proto.Marshal(&types.DiffContent{Txs: d.Txs})
		if err != nil {
			return nil, err
		}
		out[i] = types.DiffWire{
			Nonce:         d.Nonce,
			Txs:           txs,
			UserSig:       d.UserSig,
			PostStateRoot: d.PostStateRoot,
		}
	}
	return out, nil
}

func TestRefusalWalkStops(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	latest := uint64(ChallengePageMaxNonces + 2)
	st := progressState(latest)
	diffs := progressDiffs(int(latest), root)

	t.Run("cancel between pages", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		peer := &scriptedPeer{
			progressPeer: progressPeer{
				heads: []struct {
					nonce uint64
					root  []byte
					err   error
				}{{nonce: 1, root: root}},
				steps: answeredStep(),
			},
			cancel: cancel,
		}
		journal := &onePageJournal{t: t, diffs: diffs}
		accept, err := VerifyRefusedProgress(
			ctx, st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, newEvidenceGroup(t).sm, peer, journal,
			time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable,
		)
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, accept)
		require.NotContains(t, sentNonces(peer.pages), latest)
	})

	t.Run("deadline does not accept", func(t *testing.T) {
		peer := &scriptedPeer{
			progressPeer: progressPeer{
				heads: []struct {
					nonce uint64
					root  []byte
					err   error
				}{{nonce: 1, root: root}},
				steps: answeredStep(),
			},
			block: true,
		}
		journal := &onePageJournal{t: t, diffs: diffs}
		accept, err := VerifyRefusedProgress(
			context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, newEvidenceGroup(t).sm, peer, journal,
			50*time.Millisecond, 400*time.Millisecond, deadlinePassedRefused(st, 1), progressUnreachable,
		)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, accept)
	})

	t.Run("oversized diff is sent alone", func(t *testing.T) {
		wide := progressState(1)
		journal := &onePageJournal{t: t, diffs: []types.Diff{
			diffWithStoredBytes(1, root, ChallengePageMaxBytes),
		}}
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{{err: errors.New("session not found")}},
			steps: []struct {
				receipt []byte
				mempool []*types.DevshardTx
				err     error
			}{{err: errAnswered}},
		}
		accept, err := runOnePage(t, wide, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		require.Equal(t, []int{1, 0}, pageLens(peer.pages))
		require.Equal(t, []bool{false, true}, peer.sawPayload)
	})

	t.Run("tip advance skips applied nonces", func(t *testing.T) {
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{
				{nonce: 1, root: root},
				{nonce: latest, root: root},
			},
			steps: answeredStep(),
		}
		journal := &onePageJournal{t: t, diffs: diffs}
		accept, err := runOnePage(t, st, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		require.NotContains(t, sentNonces(peer.pages), latest)
		require.Contains(t, sentNonces(peer.pages), uint64(2))
	})

	t.Run("tip with a different root does not skip", func(t *testing.T) {
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{
				{nonce: 1, root: root},
				{nonce: latest, root: bytes.Repeat([]byte{9}, 32)},
			},
			steps: answeredStep(),
		}
		journal := &onePageJournal{t: t, diffs: diffs}
		accept, err := runOnePage(t, st, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		require.Contains(t, sentNonces(peer.pages), latest)
	})

	t.Run("tip past the verifier does not skip", func(t *testing.T) {
		peer := &progressPeer{
			heads: []struct {
				nonce uint64
				root  []byte
				err   error
			}{
				{nonce: 1, root: root},
				{nonce: latest + 5, root: root},
			},
			steps: answeredStep(),
		}
		journal := &onePageJournal{t: t, diffs: diffs}
		accept, err := runOnePage(t, st, peer, journal)
		require.NoError(t, err)
		require.True(t, accept)
		require.Contains(t, sentNonces(peer.pages), latest)
	})
}

func TestSessionNotFoundMatch(t *testing.T) {
	require.True(t, sessionNotFound(errors.New("session not found")))
	require.True(t, sessionNotFound(storage.ErrSessionNotFound))
	require.True(t, sessionNotFound(fmt.Errorf("get state: %w", connect.NewError(connect.CodeNotFound, errors.New("missing row")))))
	require.False(t, sessionNotFound(errors.New("storage busy: session not found in replica")))
	require.False(t, sessionNotFound(fmt.Errorf("get state: %w", connect.NewError(connect.CodeInternal, errors.New("session not found")))))
}

func TestRefusalCursorRejectsABuriedSessionPhrase(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	journal := &progressJournal{diffs: progressDiffs(4, root)}
	st := progressState(4)
	buried := errors.New("storage busy: session not found in replica")
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{err: buried}},
		steps: answeredStep(),
	}
	accept, err := VerifyRefusedProgress(
		context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
		peer, nil, newEvidenceGroup(t).sm, peer, journal,
		time.Second, 2*time.Second, deadlinePassedRefused(st, 1),
		func(err error) bool { return err != nil && !errors.Is(err, buried) },
	)
	require.NoError(t, err)
	require.True(t, accept)
	require.Empty(t, sentNonces(peer.pages))
	require.Equal(t, []bool{true}, peer.sawPayload)
}

func TestRefusalAnsweredCatchUpStillSendsPayload(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	half := ChallengePageMaxBytes/2 + 1024
	st := progressState(2)
	journal := &onePageJournal{t: t, diffs: []types.Diff{
		diffWithStoredBytes(1, root, half),
		diffWithStoredBytes(2, root, half),
	}}
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{err: errors.New("session not found")}},
		steps: []struct {
			receipt []byte
			mempool []*types.DevshardTx
			err     error
		}{{err: errAnswered}, {err: errAnswered}},
	}
	accept, err := runOnePage(t, st, peer, journal)
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, []uint64{1}, sentNonces(peer.pages))
	require.Equal(t, []bool{false, true}, peer.sawPayload)
}

func TestRefusalAnsweredCatchUpReceiptRejects(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	half := ChallengePageMaxBytes/2 + 1024
	st := progressState(2)
	g := newEvidenceGroup(t)
	confirm, sig := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)
	journal := &onePageJournal{t: t, diffs: []types.Diff{
		diffWithStoredBytes(1, root, half),
		diffWithStoredBytes(2, root, half),
	}}
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{err: errors.New("session not found")}},
		steps: []struct {
			receipt []byte
			mempool []*types.DevshardTx
			err     error
		}{
			{err: errAnswered},
			{receipt: sig, mempool: []*types.DevshardTx{confirm}},
		},
	}
	accept, err := VerifyRefusedProgress(
		context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
		peer, nil, g.sm, peer, journal,
		time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable,
	)
	require.NoError(t, err)
	require.False(t, accept)
	require.Equal(t, []bool{false, true}, peer.sawPayload)
	require.Equal(t, []uint64{1}, sentNonces(peer.pages))
}

type blockingHeadPeer struct {
	progressPeer
	blockFrom int
}

func (p *blockingHeadPeer) SessionHead(ctx context.Context) (uint64, []byte, error) {
	n := p.headN
	p.headN++
	if n >= p.blockFrom {
		<-ctx.Done()
		return 0, nil, ctx.Err()
	}
	if n >= len(p.heads) {
		n = len(p.heads) - 1
	}
	h := p.heads[n]
	return h.nonce, h.root, h.err
}

func TestRefusalProbeTimeoutDoesNotAccept(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	st := progressState(2)
	journal := &progressJournal{diffs: progressDiffs(2, root)}

	t.Run("initial head", func(t *testing.T) {
		peer := &blockingHeadPeer{blockFrom: 0}
		accept, err := VerifyRefusedProgress(
			context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, newEvidenceGroup(t).sm, peer, journal,
			50*time.Millisecond, time.Second, deadlinePassedRefused(st, 1), progressUnreachable,
		)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, accept)
		require.Empty(t, peer.pages)
	})

	t.Run("retry head", func(t *testing.T) {
		peer := &blockingHeadPeer{
			progressPeer: progressPeer{
				heads: []struct {
					nonce uint64
					root  []byte
					err   error
				}{{nonce: 1, root: root}},
				steps: answeredStep(),
			},
			blockFrom: 1,
		}
		accept, err := VerifyRefusedProgress(
			context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
			peer, nil, newEvidenceGroup(t).sm, peer, journal,
			50*time.Millisecond, time.Second, deadlinePassedRefused(st, 1), progressUnreachable,
		)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, accept)
	})
}

type wireListJournal struct {
	pages [][]types.DiffWire
	root  []byte
}

func (j *wireListJournal) Anchor(nonce uint64, root []byte) (bool, error) {
	return nonce == 1 && bytes.Equal(root, j.root), nil
}

func (j *wireListJournal) Pages(ctx context.Context, from, to uint64, _ func() uint64, fn func([]types.DiffWire, bool) (func(context.Context) error, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for i, page := range j.pages {
		if len(page) == 0 || page[0].Nonce < from || page[0].Nonce > to {
			continue
		}
		send, err := fn(page, i+1 < len(j.pages))
		page = nil
		j.pages[i] = nil
		if err != nil {
			return err
		}
		if send != nil {
			if err := send(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestRefusalCorruptCatchUpDoesNotAccept(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{nonce: 1, root: root}},
		steps: answeredStep(),
	}
	journal := &wireListJournal{
		root: root,
		pages: [][]types.DiffWire{
			{{Nonce: 2, Txs: []byte{0xff}, PostStateRoot: root}},
			{{Nonce: 3, PostStateRoot: root}},
		},
	}
	st := progressState(3)
	accept, err := VerifyRefusedProgress(
		context.Background(), st, 1, st.Inferences[1], testPayload(), nil,
		peer, nil, newEvidenceGroup(t).sm, peer, journal,
		time.Second, 2*time.Second, deadlinePassedRefused(st, 1), progressUnreachable,
	)
	require.Error(t, err)
	require.False(t, accept)
	require.Empty(t, peer.pages)
}

func TestRefusalCatchUpVersionStaysEmpty(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	half := ChallengePageMaxBytes/2 + 1024
	st := progressState(2)
	peer := &progressPeer{
		heads: []struct {
			nonce uint64
			root  []byte
			err   error
		}{{err: errors.New("session not found")}},
		steps: answeredStep(),
	}
	journal := &onePageJournal{t: t, diffs: []types.Diff{
		diffWithStoredBytes(1, root, half),
		diffWithStoredBytes(2, root, half),
	}}
	accept, err := runOnePage(t, st, peer, journal)
	require.NoError(t, err)
	require.True(t, accept)
	require.NotEmpty(t, peer.versions)
	for _, version := range peer.versions {
		require.Empty(t, version)
	}
}

func TestRefusalPageResult(t *testing.T) {
	require.Equal(t, "ok", refusalPageResult(nil, nil, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, "deadline", refusalPageResult(ctx.Err(), ctx, func(error) bool { return true }))

	tooBig := connect.NewError(connect.CodeResourceExhausted, errors.New("message size 99 is larger than configured max"))
	require.Equal(t, "too_large", refusalPageResult(tooBig, context.Background(), func(error) bool { return false }))

	require.Equal(t, "unreachable", refusalPageResult(net.ErrClosed, context.Background(), progressUnreachable))
	require.Equal(t, "answered_error", refusalPageResult(errAnswered, context.Background(), progressUnreachable))
}

func TestScreenCatchUpPageRejectsDamagedFraming(t *testing.T) {
	stored, err := proto.Marshal(&types.DiffContent{Txs: []*types.DevshardTx{{
		Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{Model: "m"}},
	}}})
	require.NoError(t, err)
	require.NoError(t, screenCatchUpPage([]types.DiffWire{{Nonce: 4, Txs: stored}}))
	require.Error(t, screenCatchUpPage([]types.DiffWire{{Nonce: 4, Txs: stored[:len(stored)-1]}}))
	require.Error(t, screenCatchUpPage([]types.DiffWire{{Nonce: 4, Txs: []byte{0x12, 0x03, 0x0a, 0x05, 0x00}}}))

	const n = 200
	txs := make([]*types.DevshardTx, n)
	for i := range txs {
		txs[i] = &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			PromptHash: []byte{byte(i)},
		}}}
	}
	many, err := proto.Marshal(&types.DiffContent{Txs: txs})
	require.NoError(t, err)
	page := []types.DiffWire{{Nonce: 4, Txs: many}}
	require.NoError(t, screenCatchUpPage(page))
	allocs := testing.AllocsPerRun(20, func() {
		if err := screenCatchUpPage(page); err != nil {
			panic(err)
		}
	})
	require.Zero(t, allocs)
}
