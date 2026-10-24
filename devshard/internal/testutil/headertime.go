package testutil

const HeightClockBlockMs = 1000

type HeightClock struct{}

func (HeightClock) HeaderTimeMs(height uint64, _ []byte) (int64, error) {
	return int64(height) * HeightClockBlockMs, nil
}
