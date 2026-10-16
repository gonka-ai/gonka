package transport

import (
	"context"

	"devshard/memwatch"
	"devshard/observability"
	"devshard/types"
)

type predecodedKey struct{}

// WithPredecodedDiffs attaches diffs a cold challenge bind already decoded.
func WithPredecodedDiffs(ctx context.Context, diffs []types.Diff) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, predecodedKey{}, diffs)
}

// PredecodedDiffs returns diffs attached by WithPredecodedDiffs.
func PredecodedDiffs(ctx context.Context) []types.Diff {
	if ctx == nil {
		return nil
	}
	diffs, _ := ctx.Value(predecodedKey{}).([]types.Diff)
	return diffs
}

// recordRefusalBody measures a refused-timeout request body. always records
// a catch-up page. A payload call records only when ctx was marked.
func recordRefusalBody(always bool, ctx context.Context, build func() ([]byte, error)) ([]byte, error) {
	track := always || memwatch.ChallengeStatsOn(ctx)
	raw, err := build()
	if err != nil || !track {
		return raw, err
	}
	observability.ObserveRefusalChallengeBytes(len(raw))
	memwatch.RecordChallengeRequest(len(raw))
	return raw, nil
}
