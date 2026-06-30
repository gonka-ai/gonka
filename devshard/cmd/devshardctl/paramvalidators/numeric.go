package paramvalidators

import "devshard"

// numericJSONValueAsUint64FromMap reads document[field] and coerces it to a uint64
// using the shared devshard primitive. Convenience helper for handlers that read
// integer fields out of the raw document by name.
func numericJSONValueAsUint64FromMap(document map[string]any, field string) (uint64, bool) {
	value, ok := document[field]
	if !ok {
		return 0, false
	}
	return devshard.JSONNumericUint64(value)
}
