package agentenvelope

import (
	"strings"
	"testing"
)

func TestValidateBeneficiary_Valid(t *testing.T) {
	cases := []string{
		"",
		"urn:customer:acme-corp",
		"a beneficiary with spaces",
		"unicode-é-is-fine",
		strings.Repeat("a", MaxBeneficiaryBytes),
	}
	for _, b := range cases {
		if err := validateBeneficiary(b); err != nil {
			t.Errorf("validateBeneficiary(%q) = %v, want nil", b, err)
		}
	}
}

func TestValidateBeneficiary_Rejected(t *testing.T) {
	// Control characters are built with string(rune(...)) so the test source
	// stays printable ASCII.
	cases := map[string]string{
		"newline":      "customer" + string(rune(0x0a)) + "injected-log-line",
		"escape":       "customer" + string(rune(0x1b)) + "attack",
		"del":          "customer" + string(rune(0x7f)) + "del",
		"c1 control":   "customer" + string(rune(0x85)) + "nel",
		"too long":     strings.Repeat("a", MaxBeneficiaryBytes+1),
		"invalid utf8": string([]byte{0x66, 0x6f, 0xff}),
	}
	for name, b := range cases {
		if err := validateBeneficiary(b); err != ErrBeneficiaryInvalid {
			t.Errorf("%s: validateBeneficiary = %v, want ErrBeneficiaryInvalid", name, err)
		}
	}
}
