package agentenvelope

import "unicode/utf8"

// validateBeneficiary checks the optional principal-attested beneficiary
// string.
//
// Per architectural principle P10 it rejects, it never transforms. The
// beneficiary is part of the principal-signed envelope. Silently stripping or
// normalizing it before logging would break the equivalence between what the
// principal signed and what the network records, so a beneficiary that cannot
// be logged verbatim and safely is treated as a malformed envelope.
//
// An empty beneficiary is allowed: the field is optional. A non-empty
// beneficiary is rejected if it exceeds the byte limit, is not valid UTF-8, or
// contains any Unicode Cc control character (the C0 range U+0000-U+001F and
// the C1 range U+007F-U+009F). The control-character rule closes the log
// injection vector.
func validateBeneficiary(b string) error {
	if b == "" {
		return nil
	}
	if len(b) > MaxBeneficiaryBytes {
		return ErrBeneficiaryInvalid
	}
	if !utf8.ValidString(b) {
		return ErrBeneficiaryInvalid
	}
	for _, r := range b {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return ErrBeneficiaryInvalid
		}
	}
	return nil
}
