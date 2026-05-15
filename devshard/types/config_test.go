package types

import "testing"

func TestSessionConfigFromEscrow_AllZerosFallsBackToDefaults(t *testing.T) {
	want := DefaultSessionConfig(16)
	got := SessionConfigFromEscrow(16, EscrowSessionFields{})

	if got != want {
		t.Fatalf("expected zero escrow to equal DefaultSessionConfig, got %+v want %+v", got, want)
	}
}

func TestSessionConfigFromEscrow_OverridesAllFields(t *testing.T) {
	got := SessionConfigFromEscrow(16, EscrowSessionFields{
		TokenPrice:        7,
		RefusalTimeout:    90,
		ExecutionTimeout:  1800,
		ValidationRate:    2500,
		CreateDevshardFee: 25_000,
		FeePerNonce:       500,
		MaxNonce:          50_000,
	})

	if got.TokenPrice != 7 {
		t.Errorf("TokenPrice: got %d, want 7", got.TokenPrice)
	}
	if got.RefusalTimeout != 90 {
		t.Errorf("RefusalTimeout: got %d, want 90", got.RefusalTimeout)
	}
	if got.ExecutionTimeout != 1800 {
		t.Errorf("ExecutionTimeout: got %d, want 1800", got.ExecutionTimeout)
	}
	if got.ValidationRate != 2500 {
		t.Errorf("ValidationRate: got %d, want 2500", got.ValidationRate)
	}
	if got.CreateDevshardFee != 25_000 {
		t.Errorf("CreateDevshardFee: got %d, want 25000", got.CreateDevshardFee)
	}
	if got.FeePerNonce != 500 {
		t.Errorf("FeePerNonce: got %d, want 500", got.FeePerNonce)
	}
	if got.MaxNonce != 50_000 {
		t.Errorf("MaxNonce: got %d, want 50000", got.MaxNonce)
	}
}

func TestSessionConfigFromEscrow_PartialOverride(t *testing.T) {
	def := DefaultSessionConfig(16)
	got := SessionConfigFromEscrow(16, EscrowSessionFields{
		ExecutionTimeout:  1800,
		CreateDevshardFee: 25_000,
		MaxNonce:          50_000,
	})

	if got.TokenPrice != def.TokenPrice {
		t.Errorf("TokenPrice should fall back to default %d, got %d", def.TokenPrice, got.TokenPrice)
	}
	if got.RefusalTimeout != def.RefusalTimeout {
		t.Errorf("RefusalTimeout should fall back to default %d, got %d", def.RefusalTimeout, got.RefusalTimeout)
	}
	if got.ExecutionTimeout != 1800 {
		t.Errorf("ExecutionTimeout: got %d, want 1800", got.ExecutionTimeout)
	}
	if got.ValidationRate != def.ValidationRate {
		t.Errorf("ValidationRate should fall back to default %d, got %d", def.ValidationRate, got.ValidationRate)
	}
	if got.CreateDevshardFee != 25_000 {
		t.Errorf("CreateDevshardFee: got %d, want 25000", got.CreateDevshardFee)
	}
	if got.FeePerNonce != def.FeePerNonce {
		t.Errorf("FeePerNonce should fall back to default %d, got %d", def.FeePerNonce, got.FeePerNonce)
	}
	if got.MaxNonce != 50_000 {
		t.Errorf("MaxNonce: got %d, want 50000", got.MaxNonce)
	}
}

func TestSessionConfigFromEscrow_PreservesGroupSizeDerivedFields(t *testing.T) {
	got := SessionConfigFromEscrow(20, EscrowSessionFields{})

	if got.VoteThreshold != 10 {
		t.Errorf("VoteThreshold: got %d, want 10 (groupSize=20 / 2)", got.VoteThreshold)
	}
}

func TestSessionConfigFromEscrow_ZeroMaxNonceFallsBackToDefault(t *testing.T) {
	// MaxNonce==0 must fall back to DefaultDevshardMaxNonce (not stay at 0).
	// This is the backward-compat path for pre-v0.2.13 escrows: hosts must
	// still enforce a cap so the chain-side VerifyDevshardSettlement doesn't
	// reject the final nonce.
	got := SessionConfigFromEscrow(16, EscrowSessionFields{TokenPrice: 1})

	if got.MaxNonce != DefaultDevshardMaxNonce {
		t.Errorf("MaxNonce: got %d, want %d (default)", got.MaxNonce, DefaultDevshardMaxNonce)
	}
}

func TestSessionConfigWithPrice_DelegatesToFromEscrow(t *testing.T) {
	withPrice := SessionConfigWithPrice(16, 42)
	fromEscrow := SessionConfigFromEscrow(16, EscrowSessionFields{TokenPrice: 42})

	if withPrice != fromEscrow {
		t.Fatalf("SessionConfigWithPrice should be equivalent to SessionConfigFromEscrow with only TokenPrice set; got %+v vs %+v", withPrice, fromEscrow)
	}
}
