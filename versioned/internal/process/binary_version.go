package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	printBinaryVersionFlag   = "--print-binary-version"
	printProtocolVersionFlag = "--print-protocol-version"
)

// childPreflight holds version metadata read before starting a child binary.
type childPreflight struct {
	// binaryLogVersion is passed to the child as DEVSHARD_BINARY_LOG_VERSION.
	// When --print-binary-version is unsupported, this is the governance slot
	// name (approved_versions.name, e.g. v2).
	binaryLogVersion string
}

// preflightChild verifies a downloaded binary when --print-* flags are
// available. Legacy binaries (released before the flags) fall back per flag:
//   - --print-binary-version missing → use slotName for DEVSHARD_BINARY_LOG_VERSION
//   - --print-protocol-version missing → trust governance slot, skip embed check
func preflightChild(binPath, slotName string) (childPreflight, error) {
	if _, err := os.Stat(binPath); err != nil {
		return childPreflight{}, fmt.Errorf("binary not found: %w", err)
	}

	binaryLogVersion, binErr := readBinaryLogVersion(binPath)
	embeddedProtocol, protoErr := readProtocolVersion(binPath)

	if binErr != nil {
		slog.Warn(
			"--print-binary-version unsupported, using slot name for DEVSHARD_BINARY_LOG_VERSION",
			"slot", slotName,
			"bin", binPath,
			"error", binErr,
		)
		binaryLogVersion = slotName
	}

	if protoErr != nil {
		slog.Warn(
			"--print-protocol-version unsupported, trusting governance slot name",
			"slot", slotName,
			"bin", binPath,
			"error", protoErr,
		)
	} else if embeddedProtocol != slotName {
		return childPreflight{}, fmt.Errorf(
			"binary protocol mismatch: slot %q embedded %q",
			slotName, embeddedProtocol,
		)
	}

	return childPreflight{binaryLogVersion: binaryLogVersion}, nil
}

// readEmbeddedVersion runs binPath with flag and returns trimmed stdout.
func readEmbeddedVersion(binPath, flag string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, flag)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", binPath, flag, err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("%s %s: empty output", binPath, flag)
	}
	return v, nil
}

func readBinaryLogVersion(binPath string) (string, error) {
	return readEmbeddedVersion(binPath, printBinaryVersionFlag)
}

func readProtocolVersion(binPath string) (string, error) {
	return readEmbeddedVersion(binPath, printProtocolVersionFlag)
}
