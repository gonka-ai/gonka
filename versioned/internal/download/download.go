package download

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var client = &http.Client{
	Timeout: 0, // no overall timeout; controlled by context
}

const InstallMetadataFilename = "install.json"

type InstallMetadata struct {
	ArchiveSHA256 string `json:"archive_sha256"`
	BinarySHA256  string `json:"binary_sha256"`
}

// Download fetches a binary from url, verifies its SHA-256 checksum, and extracts
// the named binary from the zip archive into destDir. The binary is chmod 0755.
// Binaries can be 200+ MB, so the timeout is generous (30 min).
func Download(ctx context.Context, url, expectedSHA256, destDir, binaryName string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	slog.Info("downloading binary", "url", url, "content_length", resp.ContentLength)

	tmpFile, err := os.CreateTemp("", "versiond-download-*.zip")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	written, err := io.Copy(tmpFile, io.TeeReader(resp.Body, hasher))
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("write to temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	slog.Info("download complete", "url", url, "bytes", written)

	gotHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotHash, expectedSHA256) {
		return fmt.Errorf("hash mismatch: got %s, want %s", gotHash, expectedSHA256)
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	if err := extractBinary(tmpPath, destDir, binaryName); err != nil {
		return err
	}

	binaryPath := filepath.Join(destDir, binaryName)
	binaryHash, err := HashFile(binaryPath)
	if err != nil {
		_ = os.Remove(binaryPath)
		return fmt.Errorf("hash extracted binary: %w", err)
	}

	if err := writeInstallMetadata(destDir, InstallMetadata{
		ArchiveSHA256: gotHash,
		BinarySHA256:  binaryHash,
	}); err != nil {
		_ = os.Remove(binaryPath)
		_ = os.Remove(filepath.Join(destDir, InstallMetadataFilename))
		return fmt.Errorf("write install metadata: %w", err)
	}

	return nil
}

// AtomicWriteFile writes data from r into destDir/filename via a temp file + chmod + rename.
// Prevents truncating a live binary on write failure.
func AtomicWriteFile(destDir, filename string, r io.Reader) error {
	tmp, err := os.CreateTemp(destDir, filename+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	if err := os.Rename(tmpPath, filepath.Join(destDir, filename)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func ReadInstallMetadata(destDir string) (InstallMetadata, error) {
	data, err := os.ReadFile(filepath.Join(destDir, InstallMetadataFilename))
	if err != nil {
		return InstallMetadata{}, err
	}

	var metadata InstallMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return InstallMetadata{}, fmt.Errorf("decode install metadata: %w", err)
	}
	if metadata.ArchiveSHA256 == "" {
		return InstallMetadata{}, fmt.Errorf("install metadata missing archive sha256")
	}
	if metadata.BinarySHA256 == "" {
		return InstallMetadata{}, fmt.Errorf("install metadata missing binary sha256")
	}
	return metadata, nil
}

func writeInstallMetadata(destDir string, metadata InstallMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal install metadata: %w", err)
	}
	return atomicWriteBytesFile(destDir, InstallMetadataFilename, data, 0644)
}

func atomicWriteBytesFile(destDir, filename string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(destDir, filename+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	if err := os.Rename(tmpPath, filepath.Join(destDir, filename)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractBinary(zipPath, destDir, binaryName string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		name := filepath.Base(f.Name)
		if name != binaryName {
			continue
		}
		src, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry: %w", err)
		}
		defer src.Close()
		return AtomicWriteFile(destDir, binaryName, src)
	}
	return fmt.Errorf("binary %q not found in zip", binaryName)
}
