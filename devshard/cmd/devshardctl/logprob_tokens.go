package main

import "strconv"

// isTokenID mirrors the rule a validator applies to the same field
// (common/validation.HasNonNumericTokens): a token names an id, never its text.
func isTokenID(token string) bool {
	id, err := strconv.Atoi(token)
	return err == nil && id >= 0
}
