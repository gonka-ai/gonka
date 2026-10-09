package payloads

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"common/logging"

	"github.com/klauspost/compress/zstd"
	"github.com/productscience/inference/x/inference/types"
)

type storedPayload struct {
	PromptPayload   []byte `json:"prompt_payload"`
	ResponsePayload []byte `json:"response_payload"`
}

// FileStorage stores payloads under {baseDir}/{epochId}/{escrowId}/{inferenceId}.json[.zst].
type FileStorage struct {
	baseDir      string
	compressFile bool
}

// NewFileStorage reads either suffix and writes plain JSON.
func NewFileStorage(baseDir string) *FileStorage {
	return &FileStorage{baseDir: baseDir}
}

// NewCompressingFileStorage also writes zstd. Reading accepts both either way, so the gate governs
// writing alone: a node that writes .zst hides those payloads from any older binary reading the same
// directory, and from itself after a rollback.
func NewCompressingFileStorage(baseDir string) *FileStorage {
	return &FileStorage{baseDir: baseDir, compressFile: true}
}

// sanitizeEscrowPathSegment rejects empty IDs and any escrowId that is not a
// single path segment under baseDir (no separators, ".", "..", or cleaned
// inequality). Keeps on-disk layout compatible with existing numeric IDs.
func sanitizeEscrowPathSegment(escrowId string) (string, error) {
	escrowId = strings.TrimSpace(escrowId)
	if escrowId == "" {
		return "", fmt.Errorf("payloads: empty escrowId")
	}
	if strings.Contains(escrowId, "/") || strings.Contains(escrowId, `\`) || strings.Contains(escrowId, string(filepath.Separator)) {
		return "", fmt.Errorf("payloads: invalid escrowId")
	}
	if escrowId == "." || escrowId == ".." || strings.Contains(escrowId, "..") {
		return "", fmt.Errorf("payloads: invalid escrowId")
	}
	cleaned := filepath.Base(escrowId)
	if cleaned != escrowId || cleaned == "." || cleaned == ".." {
		return "", fmt.Errorf("payloads: invalid escrowId")
	}
	return cleaned, nil
}

func (f *FileStorage) escrowDir(escrowId string, epochId uint64) (string, error) {
	segment, err := sanitizeEscrowPathSegment(escrowId)
	if err != nil {
		return "", err
	}
	dir := filepath.Clean(filepath.Join(f.baseDir, strconv.FormatUint(epochId, 10), segment))
	base := filepath.Clean(f.baseDir)
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("payloads: escrow path escapes baseDir")
	}
	return dir, nil
}

func (f *FileStorage) Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, promptPayload, responsePayload []byte) error {
	_ = ctx
	logging.Debug("Storing payload (file)", types.PayloadStorage,
		"escrowId", escrowId, "inferenceId", inferenceId, "epochId", epochId, "baseDir", f.baseDir)

	dir, err := f.escrowDir(escrowId, epochId)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("payloads: mkdir: %w", err)
	}

	data, err := json.Marshal(storedPayload{
		PromptPayload:   promptPayload,
		ResponsePayload: responsePayload,
	})
	if err != nil {
		return fmt.Errorf("payloads: marshal: %w", err)
	}

	suffix := plainSuffix
	if f.compressFile {
		compressed, compressErr := compressPayloadFile(data)
		data, suffix = namePayloadFile(data, compressed, compressErr, inferenceId)
	}

	name := strconv.FormatUint(inferenceId, 10)
	targetPath := filepath.Join(dir, name+suffix)
	tempPath := targetPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("payloads: write temp: %w", err)
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("payloads: rename: %w", err)
	}
	// The reader prefers the compressed name, so a sibling written under the other setting would
	// outrank what was just stored.
	_ = os.Remove(filepath.Join(dir, name+siblingSuffix(suffix)))
	return nil
}

// namePayloadFile picks the bytes to write and the suffix that describes them. An encoder that failed
// is stored plain, because plain JSON under the compressed name is a file the reader cannot open.
func namePayloadFile(plain, compressed []byte, compressErr error, inferenceId uint64) ([]byte, string) {
	if compressErr != nil {
		logging.Warn("Storing the payload uncompressed: zstd failed", types.PayloadStorage,
			"inferenceId", inferenceId, "error", compressErr)
		return plain, plainSuffix
	}
	return compressed, compressedSuffix
}

func siblingSuffix(suffix string) string {
	if suffix == compressedSuffix {
		return plainSuffix
	}
	return compressedSuffix
}

func (f *FileStorage) Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) ([]byte, []byte, error) {
	_ = ctx
	dir, err := f.escrowDir(escrowId, epochId)
	if err != nil {
		return nil, nil, err
	}
	data, err := readPayloadFile(dir, inferenceId)
	if err != nil {
		return nil, nil, err
	}

	var payload storedPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, fmt.Errorf("payloads: unmarshal: %w", err)
	}
	return payload.PromptPayload, payload.ResponsePayload, nil
}

func (f *FileStorage) DropEpoch(ctx context.Context, epochId uint64) error {
	_ = ctx
	epochDir := filepath.Join(f.baseDir, strconv.FormatUint(epochId, 10))
	if err := os.RemoveAll(epochDir); err != nil {
		return fmt.Errorf("payloads: remove epoch dir: %w", err)
	}
	return nil
}

var _ Storage = (*FileStorage)(nil)

// The suffix names the format; both are read, so files written before compression stay readable.
const (
	compressedSuffix    = ".json.zst"
	plainSuffix         = ".json"
	maxPayloadFileBytes = 256 << 20
)

func compressPayloadFile(data []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer, err := zstd.NewWriter(&compressed, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, fmt.Errorf("payloads: zstd writer: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("payloads: zstd write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("payloads: zstd close: %w", err)
	}
	return compressed.Bytes(), nil
}

func readPayloadFile(dir string, inferenceId uint64) ([]byte, error) {
	name := strconv.FormatUint(inferenceId, 10)
	compressed, err := os.ReadFile(filepath.Join(dir, name+compressedSuffix))
	if err == nil {
		reader, readerErr := zstd.NewReader(bytes.NewReader(compressed))
		if readerErr != nil {
			return nil, fmt.Errorf("payloads: open compressed: %w", readerErr)
		}
		defer reader.Close()
		data, readErr := io.ReadAll(io.LimitReader(reader, maxPayloadFileBytes+1))
		if readErr != nil {
			return nil, fmt.Errorf("payloads: decompress: %w", readErr)
		}
		if len(data) > maxPayloadFileBytes {
			return nil, fmt.Errorf("payloads: decompressed past the %d byte bound", maxPayloadFileBytes)
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("payloads: read file: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, name+plainSuffix))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("payloads: read file: %w", err)
	}
	return data, nil
}
