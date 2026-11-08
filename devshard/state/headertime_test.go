package state

import "devshard/internal/testutil"

func init() {
	SetFallbackHeaderTimes(testutil.HeightClock{})
}
