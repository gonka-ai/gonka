package app

import (
	"context"
	"fmt"
	"time"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/store"
	"devshard/logging"
)

func bootConfiguration(ctx context.Context, values env.Values, gatewayStore *store.Store) (*config.Config, error) {
	overrides, err := gatewayStore.LoadOverrides(ctx)
	if err != nil {
		return nil, err
	}
	clientKeys, err := gatewayStore.ActiveAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	overrides.WarnLegacyKeys()
	if values.APIKeys != nil {
		logging.Warn(env.APIKeysVariable+" is deprecated: create the keys through /v1/admin/api-keys instead; removal is planned for v7 or v8",
			logkey.Subsystem, "gateway")
	}
	configuration, err := config.Build(values, overrides, clientKeys...)
	if err != nil {
		return nil, err
	}
	if err := configuration.CheckClientKeys(); err != nil {
		logging.Warn(err.Error()+": create a key through /v1/admin/api-keys", logkey.Subsystem, "gateway")
	}
	return configuration, nil
}

// CreateAPIKey stores the key and applies it to the next request, as Reconfigure does for a setting. See ../docs/operations.md, "Client API keys".
func (o *operations) CreateAPIKey(ctx context.Context, record store.APIKeyRecord) error {
	active, err := o.store.ActiveAPIKeys(ctx)
	if err != nil {
		return err
	}
	next, err := o.buildStored(ctx, append(active, record.Key))
	if err != nil {
		return err
	}
	if err := o.store.InsertAPIKey(ctx, record); err != nil {
		return err
	}
	o.config.Swap(next)
	return nil
}

// RevokeAPIKey refuses to leave a model marked api_key with no active key.
func (o *operations) RevokeAPIKey(ctx context.Context, id string, revokedAt time.Time) error {
	records, err := o.store.ListAPIKeys(ctx)
	if err != nil {
		return err
	}
	var remaining []string
	for _, record := range records {
		if !record.Revoked() && record.ID != id {
			remaining = append(remaining, record.Key)
		}
	}
	next, err := o.buildStored(ctx, remaining)
	if err != nil {
		return err
	}
	if next.CheckClientKeys() != nil {
		return fmt.Errorf("%w: %s", api.ErrLastAPIKey, id)
	}
	if err := o.store.RevokeAPIKey(ctx, id, revokedAt); err != nil {
		return err
	}
	o.config.Swap(next)
	return nil
}

func (o *operations) buildStored(ctx context.Context, clientKeys []string) (*config.Config, error) {
	overrides, err := o.store.LoadOverrides(ctx)
	if err != nil {
		return nil, err
	}
	return config.Build(o.values, overrides, clientKeys...)
}
