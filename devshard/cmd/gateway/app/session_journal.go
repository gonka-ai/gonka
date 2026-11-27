package app

import (
	"context"

	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
)

func journaledSessions(serving registry.SessionFactory, journal store.Completions) registry.SessionFactory {
	return func(ctx context.Context, escrowID string) (registry.EscrowSession, error) {
		opened, err := serving(ctx, escrowID)
		if err != nil {
			return nil, err
		}
		if underlying := opened.UserSession(); underlying != nil {
			underlying.SetInferenceCompletionStore(journal)
		}
		return opened, nil
	}
}
