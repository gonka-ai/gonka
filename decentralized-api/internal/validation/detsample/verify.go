package detsample

// Validator-facing replay API (Stage-1 "Check 2"). This is the layer the chain
// validator calls: given one artifact position, replay the sampling and classify
// the outcome. It sits on top of the bit-verified primitives (LogprobsToWeights,
// Sha256CounterRNG, SampleCategoricalWeights).
//
// The verdict is deliberately three-valued. A replay mismatch is only *fraud*
// when the validator could faithfully reproduce the executor's computation; a
// version it does not support, or its own replay error, is *inconclusive*, never
// fraud. Collapsing these into a bool would let a validator-side or
// version-skew problem punish an honest executor (a false-fraud), which in a
// slashing system is far more costly than a missed detection.

import "fmt"

// What this validator can faithfully replay. An artifact declaring anything else
// is Inconclusive (version-unsupported), not Fraud.
const (
	SupportedContractVersion = "1.0.0"
	SupportedSeedDomain      = seedDomainTag
)

// Verdict is the classified outcome of a replay.
type Verdict string

const (
	// VerdictHonest: the replay reproduced the reported token exactly.
	VerdictHonest Verdict = "honest"
	// VerdictFraud: the validator faithfully replayed and got a different token.
	VerdictFraud Verdict = "fraud"
	// VerdictInconclusive: the validator could not faithfully replay (unsupported
	// version, greedy position, or a replay error). Must not be treated as fraud.
	VerdictInconclusive Verdict = "inconclusive"
)

// Result is a verdict plus a human-readable reason (empty for Honest).
type Result struct {
	Verdict Verdict
	Reason  string
}

// Position is one artifact position to replay. Logprobs are canonical decimal
// strings (contract §1); SeedStr is the already-composed RNG seed (contract §8
// output, e.g. from DeriveChainBoundSeed). ContractVersion / SeedDomain are the
// executor's declared versions, checked before any fraud verdict.
type Position struct {
	ContractVersion string
	SeedDomain      string
	Logprobs        map[string]string
	Temperature     string
	TopP            *string
	TopK            *int
	MinP            *string
	SeedStr         string
	ReportedToken   string
	Greedy          bool // temperature == 0 (argmax; contract §7)
}

// VerifyPosition replays one position with zero tolerance and classifies it.
// Version gating and the greedy exemption run before any fraud verdict.
func VerifyPosition(p Position) Result {
	if p.ContractVersion != SupportedContractVersion {
		return inconclusive(
			"unsupported contract version %q (validator supports %q)",
			p.ContractVersion, SupportedContractVersion)
	}
	if p.SeedDomain != SupportedSeedDomain {
		return inconclusive(
			"unsupported seed domain %q (validator supports %q)",
			p.SeedDomain, SupportedSeedDomain)
	}
	if p.Greedy {
		// Contract §7: temperature 0 is argmax and never consults the RNG, so
		// the sequence check provides no signal here; defer to the distance check.
		return inconclusive("greedy position (temperature 0): sequence check not applicable")
	}

	rng := NewFromSeedString(p.SeedStr)
	replayed, err := DecimalSampleFromLogprobs(
		p.Logprobs, rng, p.Temperature, p.TopP, p.TopK, p.MinP)
	if err != nil {
		// A replay error is a validator-side inability to reproduce, not proof
		// of fraud.
		return inconclusive("replay error: %v", err)
	}
	if replayed != p.ReportedToken {
		return Result{
			Verdict: VerdictFraud,
			Reason:  fmt.Sprintf("replayed %q but executor reported %q", replayed, p.ReportedToken),
		}
	}
	return Result{Verdict: VerdictHonest}
}

func inconclusive(format string, a ...any) Result {
	return Result{Verdict: VerdictInconclusive, Reason: fmt.Sprintf(format, a...)}
}
