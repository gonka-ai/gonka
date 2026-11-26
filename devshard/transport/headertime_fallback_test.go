package transport

import (
	"devshard/internal/testutil"
	"devshard/state"
)

func init() {
	state.SetFallbackHeaderTimes(testutil.HeightClock{})
}
