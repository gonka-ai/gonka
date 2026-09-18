package user

import (
	"devshard/host"
	"devshard/transport"
	"devshard/types"
)

const (
	catchUpChunkSize        = 200
	minimumDiffReserveBytes = 64 << 10
)

func (s *Session) catchUpBudgetLocked() int {
	if s.catchUpBudgetBytes > 0 {
		return s.catchUpBudgetBytes
	}
	return transport.CatchUpBudgetBytes
}

func countDiffsThatFit(diffs []types.Diff, budgetBytes int) int {
	maxDiffsPerChunk := min(len(diffs), catchUpChunkSize)
	usedBytes := 0
	for i := range maxDiffsPerChunk {
		usedBytes += transport.EncodedDiffSize(diffs[i])
		if usedBytes > budgetBytes {
			return i
		}
	}
	return maxDiffsPerChunk
}

func catchUpChunk(diffs []types.Diff, budgetBytes int) ([]types.Diff, error) {
	fittingCount := countDiffsThatFit(diffs, budgetBytes)
	if fittingCount == 0 {
		return nil, ErrTailTooLargeForHost
	}
	return diffs[:fittingCount], nil
}

func catchUpFitsOneBody(diffs []types.Diff, reserveBytes, budgetBytes int) bool {
	if reserveBytes >= budgetBytes {
		return false
	}
	return countDiffsThatFit(diffs, budgetBytes-reserveBytes) == len(diffs)
}

func verifyBodyReserveBytes(payload *host.InferencePayload, artifacts host.TimeoutArtifacts) int {
	reserveBytes := transport.EncodedBytesSize(artifacts.FinishTx) + transport.EncodedBytesSize(artifacts.ResponsePayload)
	if payload != nil {
		reserveBytes += transport.EncodedPromptSize(payload.Prompt, payload.Model)
	}
	return reserveBytes
}
