// Package boolvalue parses the boolean token grammar shared by devshard
// configuration and protocol boundaries.
package boolvalue

import (
	"fmt"
	"strings"
)

// Parse accepts the explicit boolean forms used by devshard. It ignores
// surrounding whitespace and letter case. An empty value is false.
func Parse(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "f", "false", "no", "off":
		return false, nil
	case "1", "t", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf(
			"invalid boolean value %q; use empty/0/f/false/no/off for false or 1/t/true/yes/on for true",
			raw,
		)
	}
}
