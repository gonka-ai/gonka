package paramvalidators

import (
	"encoding/json"
)

// numericAsUint64 / numericAsFloat64 coerce JSON-decoded numbers (and a few
// sibling types) for document validators. They live here because several
// catalog validators share them.

func numericAsUint64(v any) (uint64, bool) {
	switch x := v.(type) {
	case uint64:
		return x, true
	case float64:
		if x < 0 || x != float64(uint64(x)) {
			return 0, false
		}
		return uint64(x), true
	case int:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case int64:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case json.Number:
		n, err := x.Int64()
		if err != nil || n < 0 {
			return 0, false
		}
		return uint64(n), true
	}
	return 0, false
}
