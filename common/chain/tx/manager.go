package tx

import (
	"context"
	"fmt"
	"os"
)

// Manager signs and broadcasts chain transactions.
// It owns the signing key (via keyring) and the NATS-backed message queue.
// Callers submit messages via Send; the manager handles batching, retry, and
// deadline-block tracking internally.

type TxManagerConfig struct {
	SignerKeyName  string `yaml:"signer_key_name"`
	KeyringBackend string `yaml:"keyring_backend"` // "test" or "file"
	KeyringDir     string `yaml:"keyring_dir"`
	NatsURL        string `yaml:"nats_url"`
	// KeyringPassword is read from KEYRING_PASSWORD env var, not config file.
}

type Manager struct {
	cfg             TxManagerConfig
	rpcURL          string
	keyringPassword string // loaded from KEYRING_PASSWORD env var at construction
}

// New creates a Manager. It reads KEYRING_PASSWORD from the environment.
// cfg and rpcURL are assumed valid — config.Load guarantees this.
// Stub: keyring and NATS connections are not yet established.
func New(cfg TxManagerConfig, rpcURL string) *Manager {
	return &Manager{
		cfg:             cfg,
		rpcURL:          rpcURL,
		keyringPassword: os.Getenv("KEYRING_PASSWORD"),
	}
}

// StartInference enqueues a MsgStartInference for broadcast.
// Stub: not yet implemented.
func (m *Manager) StartInference(ctx context.Context, inferenceID, model, requester string) error {
	return fmt.Errorf("tx: StartInference not implemented")
}

// FinishInference enqueues a MsgFinishInference for broadcast.
// Stub: not yet implemented.
func (m *Manager) FinishInference(ctx context.Context, inferenceID string) error {
	return fmt.Errorf("tx: FinishInference not implemented")
}

// ReportValidation enqueues a MsgValidation for broadcast.
// Stub: not yet implemented.
func (m *Manager) ReportValidation(ctx context.Context, inferenceID string, similarityScore float64) error {
	return fmt.Errorf("tx: ReportValidation not implemented")
}
