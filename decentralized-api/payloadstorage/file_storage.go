package payloadstorage

import (
	"context"
	"decentralized-api/logging"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
)

type storedPayload struct {
	PromptPayload   []byte `json:"prompt_payload"`
	ResponsePayload []byte `json:"response_payload"`
}

// Directory structure: {baseDir}/{epochId}/{inferenceId}.json
type FileStorage struct {
	baseDir string
}

func NewFileStorage(baseDir string) *FileStorage {
	return &FileStorage{baseDir: baseDir}
}

// inferenceIdToFilename converts a base64-encoded inferenceId to a hex-encoded filename.
// This ensures filesystem-safe filenames since base64 can contain '/' characters
// which are interpreted as directory separators.
func inferenceIdToFilename(inferenceId string) string {
	return hex.EncodeToString([]byte(inferenceId))
}

// filenameToInferenceId converts a hex-encoded filename back to the original inferenceId.
// This is the inverse of inferenceIdToFilename.
func filenameToInferenceId(filename string) (string, error) {
	decoded, err := hex.DecodeString(filename)
	if err != nil {
		return "", fmt.Errorf("decode hex filename: %w", err)
	}
	return string(decoded), nil
}

// Atomic write: temp file + rename
func (f *FileStorage) Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error {
	logging.Debug("Storing payload", types.PayloadStorage, "inferenceId", inferenceId, "epochId", epochId, "baseDir", f.baseDir)
	epochDir := filepath.Join(f.baseDir, strconv.FormatUint(epochId, 10))
	if err := os.MkdirAll(epochDir, 0755); err != nil {
		return fmt.Errorf("create epoch dir: %w", err)
	}

	payload := storedPayload{
		PromptPayload:   promptPayload,
		ResponsePayload: responsePayload,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	filename := inferenceIdToFilename(inferenceId)
	targetPath := filepath.Join(epochDir, filename+".json")
	tempPath := targetPath + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("rename to target: %w", err)
	}

	return nil
}

func (f *FileStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	filename := inferenceIdToFilename(inferenceId)
	filePath := filepath.Join(f.baseDir, strconv.FormatUint(epochId, 10), filename+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			logging.Info("Payload not found", types.PayloadStorage, "inferenceId", inferenceId, "epochId", epochId, "filePath", filePath)
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("read file: %w", err)
	}

	var payload storedPayload
	// the json.Unmarshal deals with bytes correctly (base64 encoding)
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, fmt.Errorf("unmarshal payload: %w", err)
	}
	return payload.PromptPayload, payload.ResponsePayload, nil
}

func (f *FileStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	epochDir := filepath.Join(f.baseDir, strconv.FormatUint(epochId, 10))
	if err := os.RemoveAll(epochDir); err != nil {
		return fmt.Errorf("remove epoch dir: %w", err)
	}
	return nil
}

var _ PayloadStorage = (*FileStorage)(nil)
