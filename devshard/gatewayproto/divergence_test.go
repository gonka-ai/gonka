package gatewayproto

import (
	"devshard/types"
	"errors"
	"fmt"
	"testing"
)

type hiddenPostMismatch struct{}

func (hiddenPostMismatch) Error() string        { return "opaque mismatch" }
func (hiddenPostMismatch) Is(target error) bool { return target == types.ErrPostStateRootMismatch }

func TestDivergenceProfilesPreserveTextAndTypedBoundaries(t *testing.T) {
	for _, tc := range []struct {
		err             error
		legacy, current bool
	}{
		{nil, false, false},
		{errors.New("network failure"), false, false},
		{types.ErrPostStateRootMismatch, true, true},
		{fmt.Errorf("wrapped: %w", types.ErrStateHashMismatch), false, true},
		{hiddenPostMismatch{}, false, true},
		{errors.Join(errors.New("send failed"), types.ErrStateHashMismatch), false, true},
	} {
		if got := IsLegacyStateRootDivergence(tc.err); got != tc.legacy {
			t.Fatalf("legacy(%v)=%t; want %t", tc.err, got, tc.legacy)
		}
		if got := IsStateRootDivergence(tc.err); got != tc.current {
			t.Fatalf("current(%v)=%t; want %t", tc.err, got, tc.current)
		}
	}
}
