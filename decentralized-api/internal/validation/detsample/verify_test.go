package detsample

import "testing"

// positionFromVector builds an honest Position from a conformance case. An
// honest executor declares the versions this validator supports.
func positionFromVector(c vectorCaseFull) Position {
	return Position{
		ContractVersion: SupportedContractVersion,
		SeedDomain:      SupportedSeedDomain,
		Logprobs:        c.Logprobs,
		Temperature:     c.Temperature,
		TopP:            c.TopP,
		TopK:            c.TopK,
		MinP:            c.MinP,
		SeedStr:         c.SeedStr,
		ReportedToken:   c.ExpectedToken,
	}
}

// vectorCaseFull carries the fields VerifyPosition needs from a case.
type vectorCaseFull struct {
	Name          string            `json:"name"`
	Logprobs      map[string]string `json:"logprobs"`
	Temperature   string            `json:"temperature"`
	TopP          *string           `json:"top_p"`
	TopK          *int              `json:"top_k"`
	MinP          *string           `json:"min_p"`
	SeedStr       string            `json:"seed_str"`
	ExpectedToken string            `json:"expected_token"`
}

func loadFullVectors(t *testing.T) []vectorCaseFull {
	t.Helper()
	var full struct {
		Cases []vectorCaseFull `json:"cases"`
	}
	decodeVectors(t, &full)
	if len(full.Cases) == 0 {
		t.Fatal("no cases")
	}
	return full.Cases
}

// TestVerifyPositionHonest: an untampered position replays to Honest.
func TestVerifyPositionHonest(t *testing.T) {
	cases := loadFullVectors(t)
	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			r := VerifyPosition(positionFromVector(c))
			if r.Verdict != VerdictHonest {
				t.Errorf("verdict = %s (%s), want honest", r.Verdict, r.Reason)
			}
		})
	}
}

// TestVerifyPositionFraud: a tampered reported token replays to Fraud.
func TestVerifyPositionFraud(t *testing.T) {
	cases := loadFullVectors(t)
	for _, c := range cases {
		c := c
		// Pick any token id different from the honest one.
		var tampered string
		for tid := range c.Logprobs {
			if tid != c.ExpectedToken {
				tampered = tid
				break
			}
		}
		if tampered == "" {
			continue // single-token case: nothing to tamper to
		}
		t.Run(c.Name, func(t *testing.T) {
			p := positionFromVector(c)
			p.ReportedToken = tampered
			r := VerifyPosition(p)
			if r.Verdict != VerdictFraud {
				t.Errorf("verdict = %s (%s), want fraud", r.Verdict, r.Reason)
			}
		})
	}
}

// TestVerifyPositionVersionGating: unknown versions are Inconclusive, not Fraud.
func TestVerifyPositionVersionGating(t *testing.T) {
	cases := loadFullVectors(t)
	base := positionFromVector(cases[0])

	badContract := base
	badContract.ContractVersion = "9.9.9"
	if r := VerifyPosition(badContract); r.Verdict != VerdictInconclusive {
		t.Errorf("bad contract version: verdict = %s, want inconclusive", r.Verdict)
	}

	badSeed := base
	badSeed.SeedDomain = "some-other-domain"
	if r := VerifyPosition(badSeed); r.Verdict != VerdictInconclusive {
		t.Errorf("bad seed domain: verdict = %s, want inconclusive", r.Verdict)
	}

	// A tampered token under an unsupported version is still Inconclusive, never
	// Fraud — version gating must win.
	badContract.ReportedToken = "definitely-wrong"
	if r := VerifyPosition(badContract); r.Verdict != VerdictInconclusive {
		t.Errorf("tamper under bad version: verdict = %s, want inconclusive", r.Verdict)
	}
}

// TestVerifyPositionGreedy: temperature 0 is Inconclusive (no Stage-1 signal).
func TestVerifyPositionGreedy(t *testing.T) {
	cases := loadFullVectors(t)
	p := positionFromVector(cases[0])
	p.Greedy = true
	p.ReportedToken = "anything" // must not matter
	if r := VerifyPosition(p); r.Verdict != VerdictInconclusive {
		t.Errorf("greedy: verdict = %s, want inconclusive", r.Verdict)
	}
}

// TestVerifyPositionNonPositiveTemperature: a non-greedy position with
// temperature <= 0 (or unparseable) is Inconclusive, not a crash or Fraud.
func TestVerifyPositionNonPositiveTemperature(t *testing.T) {
	cases := loadFullVectors(t)
	for _, temp := range []string{"0", "0.0", "-0.5", "not-a-number"} {
		p := positionFromVector(cases[0])
		p.Greedy = false
		p.Temperature = temp
		if r := VerifyPosition(p); r.Verdict != VerdictInconclusive {
			t.Errorf("temperature %q: verdict = %s, want inconclusive", temp, r.Verdict)
		}
	}
}
