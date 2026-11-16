package inference

import (
	"testing"
	"time"

	"cosmossdk.io/x/feegrant"
	sdk "github.com/cosmos/cosmos-sdk/types"

	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

func TestInferenceOperationKeyPermsIncludesRespondDealerComplaints(t *testing.T) {
	found := false
	for _, msg := range InferenceOperationKeyPerms {
		if _, ok := msg.(*blstypes.MsgRespondDealerComplaints); ok {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("InferenceOperationKeyPerms must include MsgRespondDealerComplaints")
	}
}

func TestInferenceOperationKeyPermsIncludesDeclarePoCIntent(t *testing.T) {
	found := false
	for _, msg := range InferenceOperationKeyPerms {
		if _, ok := msg.(*types.MsgDeclarePoCIntent); ok {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("InferenceOperationKeyPerms must include MsgDeclarePoCIntent")
	}
}

func TestFeegrantRefresh(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	newExp := now.Add(365 * 24 * time.Hour)
	later := newExp.Add(time.Hour)
	ngonka := func(n int64) sdk.Coins { return sdk.NewCoins(sdk.NewInt64Coin("ngonka", n)) }
	def := DefaultMLOpsFeeAllowance.AmountOf("ngonka").Int64()

	cases := []struct {
		name     string
		existing feegrant.FeeAllowanceI
		refresh  bool
		limit    sdk.Coins
		exp      *time.Time
	}{
		{"none", nil, false, DefaultMLOpsFeeAllowance, &newExp},
		{"full and later", &feegrant.BasicAllowance{SpendLimit: ngonka(def), Expiration: &later}, false, DefaultMLOpsFeeAllowance, &later},
		{"spent", &feegrant.BasicAllowance{SpendLimit: ngonka(def - 1), Expiration: &later}, true, DefaultMLOpsFeeAllowance, &later},
		{"expired", &feegrant.BasicAllowance{SpendLimit: ngonka(def), Expiration: &now}, true, DefaultMLOpsFeeAllowance, &newExp},
		{"spent without expiration", &feegrant.BasicAllowance{SpendLimit: ngonka(1000)}, true, DefaultMLOpsFeeAllowance, nil},
		{"expires before authz", &feegrant.BasicAllowance{SpendLimit: ngonka(10 * def), Expiration: &now}, true, ngonka(10 * def), &newExp},
		{"larger kept", &feegrant.BasicAllowance{SpendLimit: ngonka(10 * def), Expiration: &later}, false, ngonka(10 * def), &later},
		{"unlimited expiring", &feegrant.BasicAllowance{Expiration: &now}, true, nil, &newExp},
		{"other denom only", &feegrant.BasicAllowance{SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("uatom", 5)), Expiration: &later}, true, DefaultMLOpsFeeAllowance, &later},
		{"periodic left alone", &feegrant.PeriodicAllowance{Basic: feegrant.BasicAllowance{SpendLimit: ngonka(1), Expiration: &now}}, false, DefaultMLOpsFeeAllowance, &newExp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refresh, limit, exp := feegrantRefresh(tc.existing, now, newExp)
			if refresh != tc.refresh {
				t.Fatalf("refresh = %v, want %v", refresh, tc.refresh)
			}
			if !limit.Equal(tc.limit) {
				t.Fatalf("limit = %s, want %s", limit, tc.limit)
			}
			if (exp == nil) != (tc.exp == nil) || (exp != nil && !exp.Equal(*tc.exp)) {
				t.Fatalf("expiration = %v, want %v", exp, tc.exp)
			}
		})
	}
}
