package detsample

import "testing"

type seedAccept struct {
	UserSeed     int64  `json:"user_seed"`
	InferenceID  string `json:"inference_id"`
	ExpectedSeed string `json:"expected_seed"`
}

type seedReject struct {
	InferenceID string `json:"inference_id"`
	Reason      string `json:"reason"`
}

type seedVectors struct {
	SeedDerivation struct {
		DomainTag         string       `json:"domain_tag"`
		Accept            []seedAccept `json:"accept"`
		RejectInferenceID []seedReject `json:"reject_inference_id"`
	} `json:"seed_derivation"`
}

func loadSeedVectors(t *testing.T) seedVectors {
	t.Helper()
	var v seedVectors
	decodeVectors(t, &v)
	if len(v.SeedDerivation.Accept) == 0 {
		t.Fatal("no seed_derivation accept cases")
	}
	return v
}

// TestChainBoundSeedAccept: the Go derivation reproduces the Python digests
// bit-for-bit (cross-language parity for the seed domain).
func TestChainBoundSeedAccept(t *testing.T) {
	v := loadSeedVectors(t)
	if v.SeedDerivation.DomainTag != seedDomainTag {
		t.Errorf("domain_tag = %q, want %q", v.SeedDerivation.DomainTag, seedDomainTag)
	}
	for _, c := range v.SeedDerivation.Accept {
		c := c
		t.Run(c.InferenceID, func(t *testing.T) {
			got, err := DeriveChainBoundSeed(c.UserSeed, c.InferenceID)
			if err != nil {
				t.Fatalf("DeriveChainBoundSeed(%d, %q): %v", c.UserSeed, c.InferenceID, err)
			}
			if got != c.ExpectedSeed {
				t.Errorf("seed(%d, %q) = %s, want %s",
					c.UserSeed, c.InferenceID, got, c.ExpectedSeed)
			}
		})
	}
}

// TestChainBoundSeedReject: every invalid inference id the reference rejects is
// also rejected here (identical fail-closed boundary).
func TestChainBoundSeedReject(t *testing.T) {
	v := loadSeedVectors(t)
	for _, c := range v.SeedDerivation.RejectInferenceID {
		c := c
		t.Run(c.Reason, func(t *testing.T) {
			if _, err := DeriveChainBoundSeed(7, c.InferenceID); err == nil {
				t.Errorf("expected rejection for %q (%s), got none", c.InferenceID, c.Reason)
			}
		})
	}
}

// TestChainBoundSeedDomainSeparation: same user_seed, different inference_id ->
// different seed (the whole point of chain binding).
func TestChainBoundSeedDomainSeparation(t *testing.T) {
	a, err := DeriveChainBoundSeed(7, "chain-abc")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveChainBoundSeed(7, "chain-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("different inference_id must yield different seed")
	}
}
