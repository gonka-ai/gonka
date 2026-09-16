package types

import (
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	MaxDevshardApprovedVersionNameLen   = 128
	MaxDevshardApprovedVersionBinaryLen = 4096
	DevshardApprovedVersionSha256Len    = 64
	MaxDevshardApprovedVersions         = 32
)

func (v DevshardApprovedVersion) Validate() error {
	if err := ValidateApprovedVersionName(v.Name); err != nil {
		return err
	}
	if v.Binary == "" {
		return fmt.Errorf("approved devshard version binary cannot be empty")
	}
	if len(v.Binary) > MaxDevshardApprovedVersionBinaryLen {
		return fmt.Errorf("approved devshard version binary exceeds maximum length of %d", MaxDevshardApprovedVersionBinaryLen)
	}
	if v.Sha256 == "" {
		return fmt.Errorf("approved devshard version sha256 cannot be empty")
	}
	if len(v.Sha256) != DevshardApprovedVersionSha256Len {
		return fmt.Errorf("approved devshard version sha256 must be %d hex characters, got %d", DevshardApprovedVersionSha256Len, len(v.Sha256))
	}
	if _, err := hex.DecodeString(v.Sha256); err != nil {
		return fmt.Errorf("approved devshard version sha256 is not valid hex: %w", err)
	}
	return nil
}

func ValidateApprovedVersionName(name string) error {
	if name == "" {
		return fmt.Errorf("approved devshard version name cannot be empty")
	}
	if len(name) > MaxDevshardApprovedVersionNameLen {
		return fmt.Errorf("approved devshard version name exceeds maximum length of %d", MaxDevshardApprovedVersionNameLen)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("approved devshard version name %q is not a valid path segment", name)
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("approved devshard version name %q must not have leading or trailing whitespace", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("approved devshard version name %q must be a single path segment", name)
	}
	return nil
}
