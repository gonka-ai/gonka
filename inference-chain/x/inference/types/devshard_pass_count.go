package types

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cosmos/gogoproto/jsonpb"
)

// normalizeRequestedPassCount maps any non-scoring value to UNSPECIFIED so a
// typo in governance JSON cannot fail Put: omitted/unknown keeps a stored
// policy, DERIVED for an already-approved name with no policy row, or
// SAMPLED for a new name.
func normalizeRequestedPassCount(c DevshardPassCount) DevshardPassCount {
	switch c {
	case DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED,
		DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED,
		DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED:
		return c
	default:
		return DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED
	}
}

// ValidateStoredDevshardPassCount rejects UNSPECIFIED: a recorded policy is
// always an explicit scoring mode.
func ValidateStoredDevshardPassCount(c DevshardPassCount) error {
	switch c {
	case DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED:
		return nil
	default:
		return fmt.Errorf("invalid stored pass_count %d", c)
	}
}

// Derived reports whether scoring uses assigned-missed-invalid (the 0.2.16
// formula). Only an explicit SAMPLED value takes the new capped-validated
// path; omitted/UNSPECIFIED at settlement is treated as DERIVED.
func (c DevshardPassCount) Derived() bool {
	return c != DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED
}

// ResolvePassCount is the write rule for Put:
//   - omitted (UNSPECIFIED) + existing policy → keep the stored value
//   - omitted (UNSPECIFIED) + new name → SAMPLED
//   - explicit SAMPLED or DERIVED → overwrite the stored policy
//   - any other requested value → same as omitted
//
// RecordVersionPassCount passes a non-nil existing for an approved name that
// has no policy row yet (DERIVED, or the scoring mode already on that row),
// so an omitted roll of a legacy name does not become SAMPLED. Genesis import
// of unstamped / params-list names still records DERIVED (those are
// pre-pass_count binaries). PassCountFor of a missing name is also DERIVED
// so live old settlements keep completed-invalid.
func ResolvePassCount(existing *DevshardPassCount, requested DevshardPassCount) (DevshardPassCount, error) {
	requested = normalizeRequestedPassCount(requested)
	if requested == DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED {
		if existing != nil {
			if err := ValidateStoredDevshardPassCount(*existing); err != nil {
				return 0, err
			}
			return *existing, nil
		}
		return DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, nil
	}
	return requested, nil
}

func (p DevshardVersionPolicy) Validate() error {
	if err := ValidateApprovedVersionName(p.Name); err != nil {
		return err
	}
	return ValidateStoredDevshardPassCount(p.PassCount)
}

// UnmarshalJSON accepts proto names, case-insensitive "derived" / "sampled",
// and 1/2. Any other JSON value becomes UNSPECIFIED rather than failing.
func (c *DevshardPassCount) UnmarshalJSON(b []byte) error {
	*c = parseDevshardPassCountJSON(b)
	return nil
}

// UnmarshalJSONPB is the cosmos proto-codec path (gogo jsonpb). Enum string
// lookup does not call UnmarshalJSON, so this must be implemented separately.
func (c *DevshardPassCount) UnmarshalJSONPB(_ *jsonpb.Unmarshaler, b []byte) error {
	return c.UnmarshalJSON(b)
}

func parseDevshardPassCountJSON(b []byte) DevshardPassCount {
	raw := strings.TrimSpace(string(b))
	if raw == "" || raw == "null" {
		return DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED
	}
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		// Compare before casting. DevshardPassCount is int32, so 4294967297
		// (2^32+1) and 4294967298 (2^32+2) would wrap to SAMPLED and DERIVED.
		return passCountFromInt64(n)
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED
	}
	return parseDevshardPassCountName(s)
}

func parseDevshardPassCountName(s string) DevshardPassCount {
	s = strings.TrimSpace(s)
	if s == "" {
		return DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return passCountFromInt64(n)
	}
	name := strings.ToUpper(s)
	name = strings.TrimPrefix(name, "DEVSHARD_PASS_COUNT_")
	switch name {
	case "SAMPLED":
		return DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED
	case "DERIVED":
		return DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED
	default:
		return DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED
	}
}

// passCountFromInt64 accepts only 0, 1, and 2. Anything else, including
// values that would wrap if cast to int32, is UNSPECIFIED (same as omitted).
func passCountFromInt64(n int64) DevshardPassCount {
	switch n {
	case int64(DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED),
		int64(DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED),
		int64(DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED):
		return DevshardPassCount(n)
	default:
		return DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED
	}
}
