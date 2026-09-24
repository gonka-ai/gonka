package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
)

func TestEpochParamsValidate_PocExchangeWithinValidationDelay(t *testing.T) {
	for _, tc := range []struct {
		exchange int64
		delay    int64
		wantErr  bool
	}{
		{exchange: 0, delay: 5, wantErr: false},
		{exchange: 2, delay: 2, wantErr: false},
		{exchange: 3, delay: 2, wantErr: true},
		{exchange: 20, delay: 5, wantErr: true},
	} {
		params := types.DefaultEpochParams()
		params.PocExchangeDuration = tc.exchange
		params.PocValidationDelay = tc.delay
		err := params.Validate()
		if tc.wantErr && err == nil {
			t.Errorf("exchange %d, delay %d: expected validation error", tc.exchange, tc.delay)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("exchange %d, delay %d: unexpected error: %v", tc.exchange, tc.delay, err)
		}
	}
}
