package journal

import (
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/internal/logkey"
)

// ChainEpoch renders the epoch or phase phaseNarrator saw change; a snapshot that never read its epoch announces none. See README.md, "Chain transitions".
func (j *Journal) ChainEpoch(epoch uint64, phase chain.EpochPhase, height, switchHeight int64) {
	j.emitLine(KindChainTransition, func(lines logSink) {
		if epoch == 0 {
			return
		}
		lines.Info("chain epoch", logkey.Epoch, epoch, logkey.Phase, phase, logkey.Height, height, logkey.SwitchHeight, switchHeight)
	})
}

// ChainRequestsBlocked renders the chain starting to refuse requests.
func (j *Journal) ChainRequestsBlocked(reason chain.BlockReason, epoch uint64, height int64) {
	j.emitLine(KindChainTransition, func(lines logSink) {
		lines.Warn("chain blocked requests", logkey.Reason, reason, logkey.Epoch, epoch, logkey.Height, height)
	})
}

// ChainRequestsUnblocked renders a block clearing, without which the block reads as permanent.
func (j *Journal) ChainRequestsUnblocked(epoch uint64, height int64) {
	j.emitLine(KindChainTransition, func(lines logSink) {
		lines.Info("chain unblocked requests", logkey.Epoch, epoch, logkey.Height, height)
	})
}

// ChainSnapshotStale renders the observer's first failed poll after a healthy one, naming the reads that failed.
func (j *Journal) ChainSnapshotStale(lastError string, epoch uint64, height int64) {
	j.emitLine(KindChainTransition, func(lines logSink) {
		lines.Warn("chain snapshot stale", logkey.Error, lastError, logkey.Epoch, epoch, logkey.Height, height)
	})
}

// ChainSnapshotRecovered renders the observer's first clean poll after a failed one.
func (j *Journal) ChainSnapshotRecovered(epoch uint64, height int64) {
	j.emitLine(KindChainTransition, func(lines logSink) {
		lines.Info("chain snapshot recovered", logkey.Epoch, epoch, logkey.Height, height)
	})
}
