package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/store"
)

type seedDevshard struct {
	EscrowID      string `json:"id"`
	PrivateKeyEnv string `json:"private_key_env"`
	Model         string `json:"model"`
	RoutePrefix   string `json:"route_prefix"`
}
type devshardRegistry interface {
	ListDevshards(ctx context.Context) ([]store.DevshardRecord, error)
	UpsertDevshard(ctx context.Context, record store.DevshardRecord) error
}

// seedDevshards leaves a known devshard alone, so a restart cannot resurrect a deactivated one.
func seedDevshards(ctx context.Context, records devshardRegistry, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var seeds []seedDevshard
	if err := json.Unmarshal([]byte(raw), &seeds); err != nil {
		return fmt.Errorf("parsing seed devshards: %w", err)
	}
	known, err := records.ListDevshards(ctx)
	if err != nil {
		return fmt.Errorf("listing devshards: %w", err)
	}
	registered := make(map[string]bool, len(known))
	for _, record := range known {
		registered[record.EscrowID] = true
	}
	for _, seed := range seeds {
		switch {
		case !isChainEscrowID(seed.EscrowID):
			return fmt.Errorf("seed devshard: %w: %q", api.ErrInvalidEscrowID, seed.EscrowID)
		case strings.TrimSpace(seed.Model) == "":
			return fmt.Errorf("seed devshard %s: model is required", seed.EscrowID)
		case strings.TrimSpace(seed.PrivateKeyEnv) == "":
			return fmt.Errorf("seed devshard %s: private_key_env is required", seed.EscrowID)
		case registered[seed.EscrowID]:
			continue
		}
		record := store.DevshardRecord{
			EscrowID:      seed.EscrowID,
			PrivateKeyEnv: seed.PrivateKeyEnv,
			Model:         seed.Model,
			Active:        true,
			RoutePrefix:   strings.TrimSpace(seed.RoutePrefix),
		}
		if err := records.UpsertDevshard(ctx, record); err != nil {
			return err
		}
	}
	return nil
}
