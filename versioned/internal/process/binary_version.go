package process

import (
	"bytes"
	"context"
	"errors"
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
	printAdminAPIVersionFlag = "--print-admin-api-version"
	printStorageModeFlag     = "--print-storage-mode"
)

var (
	errVersionFlagUnsupported   = errors.New("version flag unsupported")
	embeddedVersionProbeTimeout = 10 * time.Second
)

// childPreflight holds version metadata read before starting a child binary.
type childPreflight struct {
	// binaryLogVersion is passed to the child as DEVSHARD_BINARY_LOG_VERSION.
	// When --print-binary-version is unsupported, this is the governance slot
	// name (approved_versions.name, e.g. v2).
	binaryLogVersion  string
	adminAPISupported bool
	storageMode       string
}

// preflightChild verifies a downloaded binary when --print-* flags are
// available. Legacy binaries (released before the flags) fall back per flag:
//   - --print-binary-version missing: use slotName for DEVSHARD_BINARY_LOG_VERSION
//   - --print-protocol-version missing: trust governance slot, skip embed check
func preflightChild(binPath, slotName string) (childPreflight, error) {
	return preflightChildWithAdminProbe(binPath, slotName, false)
}

func preflightChildWithAdminProbe(binPath, slotName string, probeAdmin bool) (childPreflight, error) {
	if _, err := os.Stat(binPath); err != nil {
		return childPreflight{}, fmt.Errorf("binary not found: %w", err)
	}

	binaryLogVersion, binErr := readBinaryLogVersion(binPath)
	if binErr != nil {
		if !errors.Is(binErr, errVersionFlagUnsupported) {
			return childPreflight{}, fmt.Errorf("read binary log version: %w", binErr)
		}
		slog.Warn(
			"--print-binary-version unsupported, using slot name for DEVSHARD_BINARY_LOG_VERSION",
			"slot", slotName,
			"bin", binPath,
			"error", binErr,
		)
		binaryLogVersion = slotName
	}

	embeddedProtocol, protoErr := readProtocolVersion(binPath)
	if protoErr != nil {
		if !errors.Is(protoErr, errVersionFlagUnsupported) {
			return childPreflight{}, fmt.Errorf("read protocol version: %w", protoErr)
		}
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

	adminSupported := false
	storageMode := ""
	if probeAdmin {
		if _, adminErr := readAdminAPIVersion(binPath); adminErr != nil {
			if !errors.Is(adminErr, errVersionFlagUnsupported) {
				return childPreflight{}, fmt.Errorf("read admin api version: %w", adminErr)
			}
		} else {
			adminSupported = true
		}

		mode, modeErr := readStorageMode(binPath)
		if modeErr != nil {
			if !errors.Is(modeErr, errVersionFlagUnsupported) {
				return childPreflight{}, fmt.Errorf("read storage mode: %w", modeErr)
			}
			slog.Warn(
				"--print-storage-mode unsupported, treating devshard storage mode as legacy",
				"slot", slotName,
				"bin", binPath,
				"error", modeErr,
			)
		} else {
			storageMode = mode
		}
	}

	return childPreflight{
		binaryLogVersion:  binaryLogVersion,
		adminAPISupported: adminSupported,
		storageMode:       storageMode,
	}, nil
}

// readEmbeddedVersion runs binPath with flag and returns trimmed stdout.
func readEmbeddedVersion(binPath, flag string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), embeddedVersionProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, flag)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s %s timed out: %w", binPath, flag, ctx.Err())
		}
		output := strings.TrimSpace(stderr.String() + stdout.String())
		if isUnsupportedVersionFlagError(err, output) {
			return "", fmt.Errorf("%w: %s %s: %v: %s", errVersionFlagUnsupported, binPath, flag, err, output)
		}
		if output != "" {
			return "", fmt.Errorf("%s %s: %w: %s", binPath, flag, err, output)
		}
		return "", fmt.Errorf("%s %s: %w", binPath, flag, err)
	}
	v := strings.TrimSpace(stdout.String())
	if v == "" {
		return "", fmt.Errorf("%s %s: empty output", binPath, flag)
	}
	return v, nil
}

func isUnsupportedVersionFlagError(err error, output string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	if exitErr.ProcessState != nil && exitErr.ProcessState.ExitCode() < 0 {
		return false
	}
	msg := strings.ToLower(output)
	return strings.Contains(msg, "unknown flag") ||
		strings.Contains(msg, "flag provided but not defined") ||
		strings.Contains(msg, "unknown shorthand flag") ||
		strings.Contains(msg, "unrecognized option")
}

func readBinaryLogVersion(binPath string) (string, error) {
	return readEmbeddedVersion(binPath, printBinaryVersionFlag)
}

func readProtocolVersion(binPath string) (string, error) {
	return readEmbeddedVersion(binPath, printProtocolVersionFlag)
}

func readAdminAPIVersion(binPath string) (string, error) {
	return readEmbeddedVersion(binPath, printAdminAPIVersionFlag)
}

func readStorageMode(binPath string) (string, error) {
	return readEmbeddedVersion(binPath, printStorageModeFlag)
}
