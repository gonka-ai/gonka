package storage

import (
	"fmt"
	"strings"
)

// requireSessionVersion rejects empty session version tags at storage boundaries.
func requireSessionVersion(version string) (string, error) {
	if strings.TrimSpace(version) == "" {
		return "", ErrSessionVersionRequired
	}
	return version, nil
}

// finalizeSessionMeta ensures persisted session metadata includes a version tag.
func finalizeSessionMeta(meta *SessionMeta) error {
	if meta == nil {
		return ErrSessionVersionRequired
	}
	if strings.TrimSpace(meta.Version) == "" {
		return fmt.Errorf("%w: escrow %s", ErrSessionVersionRequired, meta.EscrowID)
	}
	return nil
}
