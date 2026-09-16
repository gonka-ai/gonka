package detsample

import (
	"fmt"
	"testing"
)

const (
	seqBaseSeed = "42|[1,2,3]"
	seqTemp     = "1.0"
)

func seqTopK() *int { k := 20; return &k } // bounded support

// seqLogprobs are four positions of canonical decimal logprob strings.
var seqLogprobs = []map[string]string{
	{"5": "-0.5", "10": "-1.2", "2": "-2.5", "100": "-3.9", "7": "-4.1"},
	{"3": "-0.3", "42": "-1.1", "9": "-2.2", "11": "-3.0", "1": "-4.5"},
	{"8": "-0.7", "6": "-1.5", "4": "-2.0", "20": "-2.9", "15": "-3.3"},
	{"12": "-0.9", "13": "-1.8", "14": "-2.4", "16": "-3.1", "17": "-3.7"},
}

// honestToken is the token an honest executor sharing the decimal path reports at
// position pos: the same pipeline + per-position seed VerifySequence replays.
func honestToken(t *testing.T, logprobs map[string]string, pos int) string {
	t.Helper()
	tok, err := DecimalSampleFromLogprobs(
		logprobs,
		NewFromSeedString(fmt.Sprintf("%s|%d", seqBaseSeed, pos)),
		seqTemp, nil, seqTopK(), nil)
	if err != nil {
		t.Fatalf("sample position %d: %v", pos, err)
	}
	return tok
}

func honestSequence(t *testing.T) []SequencePosition {
	t.Helper()
	positions := make([]SequencePosition, len(seqLogprobs))
	for i, lp := range seqLogprobs {
		positions[i] = SequencePosition{Logprobs: lp, ReportedToken: honestToken(t, lp, i)}
	}
	return positions
}

func boundedReq() SequenceRequest {
	return SequenceRequest{
		ContractVersion: SupportedContractVersion,
		SeedDomain:      SupportedSeedDomain,
		BaseSeed:        seqBaseSeed,
		Temperature:     seqTemp,
		TopK:            seqTopK(),
	}
}

func TestVerifySequenceHonest(t *testing.T) {
	r := VerifySequence(boundedReq(), honestSequence(t))
	if r.Verdict != VerdictHonest {
		t.Fatalf("verdict = %s (%s), want honest", r.Verdict, r.Reason)
	}
	if r.NHonest != len(seqLogprobs) || r.FraudPosition != -1 {
		t.Errorf("nHonest=%d fraudPos=%d, want %d and -1", r.NHonest, r.FraudPosition, len(seqLogprobs))
	}
}

func TestVerifySequenceFlagsFirstTamperedPosition(t *testing.T) {
	positions := honestSequence(t)
	positions[2].ReportedToken = "999999" // a token the RNG would not have drawn
	r := VerifySequence(boundedReq(), positions)
	if r.Verdict != VerdictFraud || r.FraudPosition != 2 {
		t.Fatalf("verdict=%s fraudPos=%d, want fraud at 2", r.Verdict, r.FraudPosition)
	}
}

func TestVerifySequenceUnboundedSupportInconclusive(t *testing.T) {
	req := boundedReq()
	req.TopK = nil // top_p-only / pure temperature: unbounded support
	p := "0.9"
	req.TopP = &p
	r := VerifySequence(req, honestSequence(t))
	if r.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %s, want inconclusive", r.Verdict)
	}
}

func TestVerifySequenceGreedyInconclusive(t *testing.T) {
	req := boundedReq()
	req.Greedy = true
	if r := VerifySequence(req, honestSequence(t)); r.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %s, want inconclusive", r.Verdict)
	}
}

func TestVerifySequenceVersionGating(t *testing.T) {
	req := boundedReq()
	req.ContractVersion = "9.9.9"
	if r := VerifySequence(req, honestSequence(t)); r.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %s, want inconclusive", r.Verdict)
	}
}

func TestVerifySequenceMissingLogprobsIsInconclusiveNotFraud(t *testing.T) {
	positions := honestSequence(t)
	positions[1].Logprobs = nil // no replay data at this position
	r := VerifySequence(boundedReq(), positions)
	if r.Verdict != VerdictHonest {
		t.Fatalf("verdict = %s, want honest (missing != fraud)", r.Verdict)
	}
	if r.NInconclusive != 1 || r.NHonest != len(seqLogprobs)-1 {
		t.Errorf("nInconclusive=%d nHonest=%d, want 1 and %d", r.NInconclusive, r.NHonest, len(seqLogprobs)-1)
	}
}

func TestSupportIsBounded(t *testing.T) {
	k := 20
	zero := 0
	mp := "0.02"
	mpZero := "0"
	cases := []struct {
		topK *int
		minP *string
		want bool
	}{
		{&k, nil, true},
		{nil, &mp, true},
		{nil, nil, false},       // pure temperature
		{&zero, &mpZero, false}, // disabled sentinels
	}
	for i, c := range cases {
		if got := supportIsBounded(c.topK, c.minP); got != c.want {
			t.Errorf("case %d: supportIsBounded = %v, want %v", i, got, c.want)
		}
	}
}
