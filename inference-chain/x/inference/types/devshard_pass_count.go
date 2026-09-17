package types

import "fmt"

// ValidateDevshardPassCount accepts omitted (UNSPECIFIED) plus the two scoring
// modes. Stored policies must use ValidateStoredDevshardPassCount.
func ValidateDevshardPassCount(c DevshardPassCount) error {
	switch c {
	case DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED,
		DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED,
		DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED:
		return nil
	default:
		return fmt.Errorf("invalid pass_count %d", c)
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
func ResolvePassCount(existing *DevshardPassCount, requested DevshardPassCount) (DevshardPassCount, error) {
	if err := ValidateDevshardPassCount(requested); err != nil {
		return 0, err
	}
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
