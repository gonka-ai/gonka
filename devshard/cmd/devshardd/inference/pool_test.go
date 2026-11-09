package inference

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonvalidation "common/validation"
)

func TestFlipPool_SumsTheWindowPerExecutorAndModel(t *testing.T) {
	pool := newFlipPool(3, time.Hour)
	pool.Observe("a", "m", 100, 4)
	pool.Observe("a", "m", 100, 5)
	pool.Observe("b", "m", 100, 50)
	answers, steps, count := pool.Observe("a", "m", 100, 6)
	require.Equal(t, 3, answers)
	require.Equal(t, 300, steps)
	require.Equal(t, 15, count)
	// The window keeps the last three answers.
	answers, steps, count = pool.Observe("a", "m", 100, 7)
	require.Equal(t, 3, answers)
	require.Equal(t, 300, steps)
	require.Equal(t, 18, count)
}

func TestFlipPool_DropsOldAnswers(t *testing.T) {
	pool := newFlipPool(32, time.Hour)
	at := time.Unix(0, 0)
	pool.now = func() time.Time { return at }
	pool.Observe("a", "m", 100, 10)
	at = at.Add(2 * time.Hour)
	answers, steps, count := pool.Observe("a", "m", 100, 1)
	require.Equal(t, 1, answers)
	require.Equal(t, 100, steps)
	require.Equal(t, 1, count)
}

// An int4 substitute of DeepSeek is off the winner at ~7.5 % of positions
// against an honest 3.5-4 %: each 250-position answer passes the 5 % bound,
// the sum of eight does not; eight honest answers pass.
func TestFlipPool_CatchesBySumWhatOneAnswerPasses(t *testing.T) {
	th := commonvalidation.EchoThresholdsFor("deepseek-ai/DeepSeek-V4-Flash-0731")
	pool := newFlipPool(32, time.Hour)
	var steps, count int
	for i := 0; i < 8; i++ {
		require.True(t, th.CountPasses(250, 19, 2.576), "one answer at 7.6 % passes")
		_, steps, count = pool.Observe("int4", "m", 250, 19)
	}
	require.False(t, th.CountPasses(steps, count, pooledQuantile))
	honest := newFlipPool(32, time.Hour)
	for i := 0; i < 8; i++ {
		_, steps, count = honest.Observe("fp8", "m", 250, 10)
	}
	require.True(t, th.CountPasses(steps, count, pooledQuantile))
}
