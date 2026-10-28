package chain

import "context"

const pocLeadBlocks = 2

func preservationEpoch(epoch epochInfo, blocked bool, reason BlockReason) (epochInfo, bool, BlockReason) {
	if blocked || epoch.NextPoCStartBlockHeight <= 0 || epoch.BlockHeight <= 0 {
		return epoch, blocked, reason
	}
	if epoch.BlockHeight < epoch.NextPoCStartBlockHeight-pocLeadBlocks || epoch.BlockHeight >= epoch.NextPoCStartBlockHeight {
		return epoch, blocked, reason
	}
	upcoming := epoch
	upcoming.PoCStartBlockHeight = epoch.NextPoCStartBlockHeight
	return upcoming, true, BlockReasonPoC
}

// resolvePreservation picks the node-preservation rule for this poll. See README.md, "Which nodes count as preserved".
func (o *PhaseObserver) resolvePreservation(ctx context.Context, epoch epochInfo, blocked bool, reason BlockReason) (preservationMode, preservedSnapshotState, error) {
	if !blocked && !epoch.IsConfirmationPoCActive {
		return preservationModeAll, preservedSnapshotState{}, nil
	}
	preservedNodes, status, err := o.fetchPreservedSnapshot(ctx, preservedSnapshotAnchor(epoch, reason))
	switch {
	case status == preservedSnapshotCurrent:
		return preservationModeSnapshot, preservedNodes, nil
	case status == preservedSnapshotMissingCurrent && allowAllParticipantsUntilSnapshot(epoch, reason):
		return preservationModeAll, preservedSnapshotState{}, nil
	default:
		return preservationModeLegacy, preservedSnapshotState{}, err
	}
}

// preservedSnapshotAnchor is the height a preserved snapshot must match to count as current; 0 skips the check.
func preservedSnapshotAnchor(epoch epochInfo, reason BlockReason) int64 {
	switch reason {
	case BlockReasonConfirmationPoC:
		return epoch.ConfirmationPoCTriggerHeight
	case BlockReasonPoC:
		return epoch.PoCStartBlockHeight
	}
	if epoch.IsConfirmationPoCActive {
		return epoch.ConfirmationPoCTriggerHeight
	}
	return 0
}

// allowAllParticipantsUntilSnapshot reports the grace period, when the matching preserved snapshot intentionally does not exist yet.
func allowAllParticipantsUntilSnapshot(epoch epochInfo, reason BlockReason) bool {
	return reason == BlockReasonConfirmationPoC && epoch.ConfirmationPoCPhase == ConfirmationPoCGracePeriod
}

// fetchPreservedSnapshot separates "no snapshot for this episode" from "could not be reached"; only the second is an error.
func (o *PhaseObserver) fetchPreservedSnapshot(ctx context.Context, expectedAnchor int64) (preservedSnapshotState, preservedSnapshotStatus, error) {
	if o.chain == nil {
		return preservedSnapshotState{}, preservedSnapshotUnavailable, nil
	}
	snapshot, found, err := o.chain.PreservedNodes(ctx)
	if err != nil {
		return preservedSnapshotState{}, preservedSnapshotUnavailable, err
	}
	if !found {
		return preservedSnapshotState{}, preservedSnapshotMissingCurrent, nil
	}
	if expectedAnchor > 0 && snapshot.EpisodeAnchorHeight != expectedAnchor {
		return preservedSnapshotState{}, preservedSnapshotMissingCurrent, nil
	}
	return newPreservedSnapshotState(snapshot), preservedSnapshotCurrent, nil
}
