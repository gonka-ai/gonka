package payloads

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MigrateFilePayloadsToPostgres walks {baseDir}/{epoch}/{escrow}/{inference}.json
// files and stores each into dest (typically Postgres). Idempotent: dest Store
// uses ON CONFLICT DO NOTHING. After a successful full walk, migrated epoch
// trees are quarantined (renamed *.migrated.<ts>) so HA Postgres-only boots
// do not leave invisible local copies.
//
// Returns the number of payload files copied (including ones already present
// on dest). Boot must wait for this to finish before serving traffic.
func MigrateFilePayloadsToPostgres(ctx context.Context, baseDir string, dest Storage) (int, error) {
	if dest == nil {
		return 0, fmt.Errorf("payloads migrate: dest is nil")
	}
	info, err := os.Stat(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("payloads migrate: %s is not a directory", baseDir)
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0, err
	}

	copied := 0
	var epochDirs []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		epochID, err := strconv.ParseUint(ent.Name(), 10, 64)
		if err != nil {
			continue
		}
		epochPath := filepath.Join(baseDir, ent.Name())
		n, err := migrateEpochPayloads(ctx, dest, epochPath, epochID)
		if err != nil {
			return copied, err
		}
		copied += n
		epochDirs = append(epochDirs, epochPath)
	}

	if len(epochDirs) == 0 {
		return copied, nil
	}
	if err := quarantinePayloadEpochDirs(epochDirs); err != nil {
		return copied, err
	}
	slog.Info("payload storage: quarantined file payloads after postgres migrate",
		"dir", baseDir, "epochs", len(epochDirs), "files", copied)
	return copied, nil
}

func migrateEpochPayloads(ctx context.Context, dest Storage, epochPath string, epochID uint64) (int, error) {
	escrowEntries, err := os.ReadDir(epochPath)
	if err != nil {
		return 0, fmt.Errorf("read epoch %d: %w", epochID, err)
	}
	copied := 0
	for _, escrowEnt := range escrowEntries {
		if !escrowEnt.IsDir() {
			continue
		}
		escrowID := escrowEnt.Name()
		if escrowID == "" || strings.Contains(escrowID, "..") {
			continue
		}
		escrowPath := filepath.Join(epochPath, escrowID)
		files, err := os.ReadDir(escrowPath)
		if err != nil {
			return copied, fmt.Errorf("read escrow %s: %w", escrowID, err)
		}
		for _, fileEnt := range files {
			if fileEnt.IsDir() || !strings.HasSuffix(fileEnt.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(fileEnt.Name(), ".json")
			inferenceID, err := strconv.ParseUint(name, 10, 64)
			if err != nil {
				continue
			}
			path := filepath.Join(escrowPath, fileEnt.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return copied, fmt.Errorf("read %s: %w", path, err)
			}
			var payload storedPayload
			if err := json.Unmarshal(data, &payload); err != nil {
				return copied, fmt.Errorf("unmarshal %s: %w", path, err)
			}
			if err := dest.Store(ctx, escrowID, inferenceID, epochID, payload.PromptPayload, payload.ResponsePayload); err != nil {
				return copied, fmt.Errorf("store %s/%d/%d: %w", escrowID, inferenceID, epochID, err)
			}
			copied++
		}
	}
	return copied, nil
}

func quarantinePayloadEpochDirs(epochDirs []string) error {
	stamp := time.Now().Unix()
	for _, dir := range epochDirs {
		target := fmt.Sprintf("%s.migrated.%d", dir, stamp)
		if err := os.Rename(dir, target); err != nil {
			return fmt.Errorf("quarantine %s: %w", dir, err)
		}
	}
	return nil
}
