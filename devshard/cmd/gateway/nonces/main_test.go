package nonces

import (
	"testing"

	"devshard/internal/testutil"
	"devshard/state"
)

func TestMain(m *testing.M) {
	state.SetFallbackHeaderTimes(testutil.HeightClock{})
	m.Run()
}
