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
// policy, or DERIVED for a new name.
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

// Derived reports whether scoring uses assigned-missed-invalid rather than
// HostStats.validated. Only an explicit SAMPLED value takes the new path;
// omitted/UNSPECIFIED is treated as DERIVED.
func (c DevshardPassCount) Derived() bool {
	return c != DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED
}

// ResolvePassCount is the write rule for Put / genesis:
//   - omitted (UNSPECIFIED) + existing policy → keep the stored value
//   - omitted (UNSPECIFIED) + new name → DERIVED
//   - explicit SAMPLED or DERIVED → overwrite the stored policy
//   - any other requested value → same as omitted
func ResolvePassCount(existing *DevshardPassCount, requested DevshardPassCount) (DevshardPassCount, error) {
	requested = normalizeRequestedPassCount(requested)
	if requested == DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED {
		if existing != nil {
			if err := ValidateStoredDevshardPassCount(*existing); err != nil {
				return 0, err
			}
			return *existing, nil
		}
		return DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, nil
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
		return normalizeRequestedPassCount(DevshardPassCount(n))
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
	if n, err := strconv.ParseInt(s, 10, 32); err == nil {
		return normalizeRequestedPassCount(DevshardPassCount(n))
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
