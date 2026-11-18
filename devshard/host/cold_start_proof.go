package host

import (
	"context"
	"errors"

	"devshard/logging"
	"devshard/types"
)

var (
	errFirstPageRead  = errors.New("first challenge page read")
	errColdUnbindable = errors.New("cold executor cannot bind from the first challenge page")
)

type coldProofCheckKey struct{}

type coldProofCheck struct{ done bool }

func withColdProofCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, coldProofCheckKey{}, &coldProofCheck{})
}

func checkColdProofPage(ctx context.Context, st types.EscrowState, page []types.DiffWire, more bool) error {
	c, _ := ctx.Value(coldProofCheckKey{}).(*coldProofCheck)
	if c == nil || c.done {
		return nil
	}
	c.done = true
	if len(page) == 0 || page[0].Nonce != 1 {
		return nil
	}
	bindable, err := pageStartProofBindable(st, page)
	if err != nil {
		return err
	}
	if walk := coldWalkFrom(ctx); walk != nil {
		walk.gate.noteStartProof(bindable, more || bindable)
	}
	if !bindable {
		return errColdUnbindable
	}
	return nil
}

func pageStartProofBindable(st types.EscrowState, page []types.DiffWire) (bool, error) {
	diffs := make([]types.Diff, 0, len(page))
	for _, w := range page {
		d, err := types.DecodeDiffWire(w)
		if err != nil {
			return false, err
		}
		diffs = append(diffs, d)
	}
	version, err := types.StartProofVersion(diffs)
	return err == nil && version == st.StateRootAndProtocolVersion, nil
}

func logColdUnbindable(st types.EscrowState, inferenceID uint64) {
	logging.Info("refused timeout rejected: cold executor cannot bind from the first challenge page",
		"escrow_id", st.EscrowID, "inference_id", inferenceID, "latest_nonce", st.LatestNonce)
}

func cachedColdStartProof(ctx context.Context) (bindable, known bool) {
	if walk := coldWalkFrom(ctx); walk != nil {
		return walk.gate.startProof()
	}
	return false, false
}

func coldStartProofBindable(ctx context.Context, st types.EscrowState, journal RefusalJournal) (bool, error) {
	walk := coldWalkFrom(ctx)
	bindable, final, err := readColdStartProof(ctx, st, journal)
	if err != nil {
		return false, err
	}
	if walk != nil {
		walk.gate.noteStartProof(bindable, final)
	}
	return bindable, nil
}

func readColdStartProof(ctx context.Context, st types.EscrowState, journal RefusalJournal) (bindable, final bool, err error) {
	if st.LatestNonce == 0 {
		return false, false, nil
	}
	err = journal.Pages(ctx, 1, st.LatestNonce, nil, func(page []types.DiffWire, more bool) (func(context.Context) error, error) {
		var pageErr error
		bindable, pageErr = pageStartProofBindable(st, page)
		if pageErr != nil {
			return nil, pageErr
		}
		final = more
		return nil, errFirstPageRead
	})
	if err != nil && !errors.Is(err, errFirstPageRead) {
		return false, false, err
	}
	return bindable, final || bindable, nil
}

type participationEvidence interface {
	ExecutorParticipated(slot uint32) bool
}

func executorProvedBound(ev EvidenceVerifier, st types.EscrowState, slot uint32) bool {
	if p, ok := ev.(participationEvidence); ok && p.ExecutorParticipated(slot) {
		return true
	}
	return types.ExecutorParticipated(st, slot)
}
