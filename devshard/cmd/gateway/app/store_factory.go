package app

import (
	"context"

	"common/storage/mode"

	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/store"
	"devshard/cmd/gateway/store/postgres"
	"devshard/cmd/gateway/store/sqlite"
	"devshard/logging"
)

// storeChoice is the selected backend together with the settings only that backend reads.
type storeChoice interface {
	open(ctx context.Context) (store.Store, error)
}

type sqliteChoice struct{ directory string }

type postgresChoice struct{ instanceID string }

// chooseStore reads storageDir only when the SQLite backend is selected.
func chooseStore(storageDir *string) (storeChoice, error) {
	backend, err := env.ResolveStorageBackend()
	if err != nil {
		return nil, err
	}
	if backend.Mode == mode.Postgres {
		return postgresChoice{instanceID: backend.InstanceID}, nil
	}
	directory, err := resolveStorageDir(storageDir)
	if err != nil {
		return nil, err
	}
	return sqliteChoice{directory: directory}, nil
}

func (choice sqliteChoice) open(context.Context) (store.Store, error) {
	if ignored := env.IgnoredPostgresVariables(); len(ignored) > 0 {
		logging.Warn("Postgres settings are ignored: the gateway store is SQLite unless DEVSHARD_STORAGE_MODE is postgres",
			logkey.Subsystem, "gateway", logkey.IgnoredVariables, ignored)
	}
	return sqlite.Open(choice.directory)
}

func (choice postgresChoice) open(ctx context.Context) (store.Store, error) {
	logging.Info("the gateway store is Postgres",
		logkey.Subsystem, "gateway", logkey.StorageMode, string(mode.Postgres), logkey.InstanceID, choice.instanceID)
	return postgres.Open(ctx, postgres.Options{})
}
