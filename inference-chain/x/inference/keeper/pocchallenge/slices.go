package pocchallenge

type SliceRange struct {
	Index  uint32
	Start  int64
	End    int64 // exclusive
	Length int64
}

// CountedSlices returns complete slices plus a last incomplete slice if it is
// at least MinPunishableSegmentBlocks. A sealed segment shorter than that
// has no counted slices and is AUTO_PASSED.
func CountedSlices(startHeight, sealHeight, sliceBlocks int64) []SliceRange {
	if sliceBlocks <= 0 || sealHeight <= startHeight {
		return nil
	}
	length := sealHeight - startHeight
	if length < MinPunishableSegmentBlocks {
		return nil
	}
	var out []SliceRange
	var idx uint32
	for cursor := startHeight; cursor < sealHeight; idx++ {
		end := cursor + sliceBlocks
		if end > sealHeight {
			end = sealHeight
		}
		part := end - cursor
		if part == sliceBlocks || part >= MinPunishableSegmentBlocks {
			out = append(out, SliceRange{Index: idx, Start: cursor, End: end, Length: part})
		}
		cursor = end
	}
	return out
}

func CurrentSliceIndex(height, startHeight, sliceBlocks int64) uint32 {
	if sliceBlocks <= 0 || height < startHeight {
		return 0
	}
	return uint32((height - startHeight) / sliceBlocks)
}

func LastSliceIndex(startHeight, sealHeight, sliceBlocks int64) uint32 {
	if sliceBlocks <= 0 || sealHeight <= startHeight {
		return 0
	}
	return uint32((sealHeight - 1 - startHeight) / sliceBlocks)
}

func SafetyWindowHeight(nextPoCStart, safetyWindow int64) int64 {
	return nextPoCStart - safetyWindow
}
