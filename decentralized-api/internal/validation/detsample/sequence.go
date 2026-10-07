package detsample

// Sequence-level Stage-1 replay. VerifyPosition classifies one position; this
// layer replays a whole response and aggregates a three-valued verdict, mirroring
// the Python serving-side orchestrator (validation_sampling.verify_sequence) so
// the chain (Go) and vLLM (Python) reach the same verdict from the same artifact.
//
// RNG semantics: one seed per position (Decision B = per-position). The sequence
// composes each position's seed as fmt.Sprintf("%s|%d", BaseSeed, i); a fresh RNG
// per position keeps the variable draw-count of rejection sampling at one position
// from desyncing the replay of the rest of the sequence. This matches the Python
// side byte-for-byte.

import "fmt"

// SequencePosition is one position's replay data within a sequence. Unlike
// Position it carries no SeedStr — the sequence derives it from BaseSeed. A nil
// Logprobs marks a position with no replay data (Inconclusive, not Fraud).
type SequencePosition struct {
	Logprobs      map[string]string
	ReportedToken string
}

// SequenceRequest holds the request-level parameters shared by every position.
type SequenceRequest struct {
	ContractVersion string
	SeedDomain      string
	BaseSeed        string
	Temperature     string
	TopP            *string
	TopK            *int
	MinP            *string
	Greedy          bool // temperature == 0 for the whole request (contract §7)
}

// SequenceResult aggregates a whole-response replay.
type SequenceResult struct {
	Verdict       Verdict
	FraudPosition int // first fraud position; -1 if none
	NHonest       int
	NInconclusive int
	Reason        string
}

// VerifySequence replays a response position-by-position and aggregates:
//
//	any position Fraud   -> Fraud (reports the first such position)
//	else >= 1 Honest     -> Honest (Inconclusive positions defer to Stage-2)
//	else all Inconclusive -> Inconclusive (Stage-1 carries no signal)
//
// Version/seed-domain/greedy gating and the unbounded-support gate run before any
// per-position replay, so a request the validator structurally cannot cover never
// yields Fraud.
func VerifySequence(req SequenceRequest, positions []SequencePosition) SequenceResult {
	if req.ContractVersion != SupportedContractVersion {
		return seqInconclusive("unsupported contract version %q (validator supports %q)",
			req.ContractVersion, SupportedContractVersion)
	}
	if req.SeedDomain != SupportedSeedDomain {
		return seqInconclusive("unsupported seed domain %q (validator supports %q)",
			req.SeedDomain, SupportedSeedDomain)
	}
	if req.Greedy {
		return seqInconclusive("greedy (temperature 0): sequence check not applicable")
	}
	if !supportIsBounded(req.TopK, req.MinP) {
		// top_p alone (especially high temperature) and pure-temperature sampling
		// have unbounded support: the nucleus can exceed the signed top-K, so the
		// reported set cannot faithfully reproduce the filter. Defer to Stage-2.
		return seqInconclusive("unbounded support (no top_k/min_p): deferred to distance check")
	}

	nHonest, nInconclusive := 0, 0
	for i, pos := range positions {
		if pos.Logprobs == nil {
			nInconclusive++
			continue
		}
		r := VerifyPosition(Position{
			ContractVersion: req.ContractVersion,
			SeedDomain:      req.SeedDomain,
			Logprobs:        pos.Logprobs,
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			TopK:            req.TopK,
			MinP:            req.MinP,
			SeedStr:         fmt.Sprintf("%s|%d", req.BaseSeed, i),
			ReportedToken:   pos.ReportedToken,
		})
		switch r.Verdict {
		case VerdictFraud:
			return SequenceResult{
				Verdict:       VerdictFraud,
				FraudPosition: i,
				NHonest:       nHonest,
				NInconclusive: nInconclusive,
				Reason:        r.Reason,
			}
		case VerdictHonest:
			nHonest++
		default:
			nInconclusive++
		}
	}

	if nHonest > 0 {
		return SequenceResult{
			Verdict:       VerdictHonest,
			FraudPosition: -1,
			NHonest:       nHonest,
			NInconclusive: nInconclusive,
		}
	}
	return SequenceResult{
		Verdict:       VerdictInconclusive,
		FraudPosition: -1,
		NInconclusive: nInconclusive,
		Reason:        "no replayable position (all inconclusive)",
	}
}

// supportIsBounded reports whether the request's filter pins the support to a
// finite, exactly-reproducible set: true iff top_k or min_p is active. Mirrors
// validation_sampling._support_is_bounded (Python).
func supportIsBounded(topK *int, minP *string) bool {
	if topK != nil && *topK > 0 {
		return true
	}
	if minP != nil {
		if v, err := parseDec(*minP); err == nil && v.Cmp(zeroDecimal) > 0 {
			return true
		}
	}
	return false
}

func seqInconclusive(format string, a ...any) SequenceResult {
	return SequenceResult{
		Verdict:       VerdictInconclusive,
		FraudPosition: -1,
		Reason:        fmt.Sprintf(format, a...),
	}
}
