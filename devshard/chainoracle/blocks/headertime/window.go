package headertime

// EpochFloor is the lowest height still kept: the first height of epoch
// current−1. currentEpoch 0 or 1, or an unknown previous start, keeps the
// whole map (floor 0). Entries of epoch < currentEpoch−1 are dropped.
func EpochFloor(currentEpoch, previousEpochStart uint64) uint64 {
	if currentEpoch <= 1 || previousEpochStart == 0 {
		return 0
	}
	return previousEpochStart
}
