package payloads

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

type storedPayload struct {
	PromptPayload   []byte `json:"prompt_payload"`
	ResponsePayload []byte `json:"response_payload"`
}

// FileStorage stores payloads as JSON files under
// {baseDir}/{epochId}/{escrowId}/{inferenceId}.json .
type FileStorage struct {
	baseDir string
}

func NewFileStorage(baseDir string) *FileStorage {
	return &FileStorage{baseDir: baseDir}
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

	targetPath := filepath.Join(dir, strconv.FormatUint(inferenceId, 10)+".json")
	tempPath := targetPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("payloads: write temp: %w", err)
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("payloads: rename: %w", err)
	}
	return nil
}

func (f *FileStorage) Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) ([]byte, []byte, error) {
	_ = ctx
	dir, err := f.escrowDir(escrowId, epochId)
	if err != nil {
		return nil, nil, err
	}
	filePath := filepath.Join(dir, strconv.FormatUint(inferenceId, 10)+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("payloads: read file: %w", err)
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
