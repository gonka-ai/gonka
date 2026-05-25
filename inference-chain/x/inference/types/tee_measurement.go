package types

import (
	"encoding/hex"
	"fmt"
)

func CanonicalMeasurement(measurement string) (string, error) {
	stripped := measurement
	if len(stripped) >= 2 && (stripped[0:2] == "0x" || stripped[0:2] == "0X") {
		stripped = stripped[2:]
	}
	if stripped == "" {
		return "", fmt.Errorf("measurement is empty")
	}
	if len(stripped)%2 != 0 {
		return "", fmt.Errorf("measurement %q has odd hex length", measurement)
	}
	if _, err := hex.DecodeString(stripped); err != nil {
		return "", fmt.Errorf("measurement %q is not valid hex: %v", measurement, err)
	}

	out := make([]byte, len(stripped))
	for i := 0; i < len(stripped); i++ {
		c := stripped[i]
		if c >= 'A' && c <= 'F' {
			c = c - 'A' + 'a'
		}
		out[i] = c
	}
	return string(out), nil
}

func IsCanonicalMeasurement(s string) bool {
	if s == "" || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
