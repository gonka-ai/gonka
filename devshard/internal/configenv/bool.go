// Package configenv defines environment-value conventions shared by devshard
// processes.
package configenv

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseBool parses the boolean grammar used by devshard environment settings.
// It ignores surrounding whitespace and letter case. An empty value is false.
func ParseBool(raw string) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "":
		return false, nil
	case "yes", "on":
		return true, nil
	case "no", "off":
		return false, nil
	}

	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf(
			"%q is not a devshard boolean; use empty/0/f/false/no/off or 1/t/true/yes/on",
			raw,
		)
	}
	return parsed, nil
}
