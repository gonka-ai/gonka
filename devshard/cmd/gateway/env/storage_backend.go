package env

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"common/storage/mode"
	"common/storage/pgtimeouts"
)

// InstanceIDVariable names this process in the boot log.
const (
	InstanceIDVariable   = "GATEWAY_INSTANCE_ID"
	postgresHostVariable = "PGHOST"
)

var postgresVariables = []string{
	postgresHostVariable, "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE",
	pgtimeouts.EnvConnectTimeout, pgtimeouts.EnvOperationTimeout, "PG_POOL_MAX_CONNS",
	"PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGAPPNAME", "PGOPTIONS", "PGTARGETSESSIONATTRS",
}

// StorageBackend is where the gateway keeps its control-plane state. See ../docs/operations.md, "Storing state in Postgres".
type StorageBackend struct {
	Mode       mode.Mode
	InstanceID string
}

// ResolveStorageBackend reads DEVSHARD_STORAGE_MODE: unset or sqlite is the local store, postgres is a shared one, anything else is refused, hybrid and auto included. The connection comes from the libpq PG* variables.
func ResolveStorageBackend() (StorageBackend, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(mode.EnvStorageMode)))
	switch mode.Mode(raw) {
	case "", mode.SQLite:
		return StorageBackend{Mode: mode.SQLite}, nil
	case mode.Postgres:
		return resolvePostgres()
	default:
		return StorageBackend{}, fmt.Errorf("%s %q is not supported by the gateway (want sqlite or postgres)", mode.EnvStorageMode, raw)
	}
}

func resolvePostgres() (StorageBackend, error) {
	var problems []error
	instanceID := strings.TrimSpace(os.Getenv(InstanceIDVariable))
	if instanceID == "" {
		problems = append(problems, fmt.Errorf("%s is required when %s=postgres", InstanceIDVariable, mode.EnvStorageMode))
	}
	if strings.TrimSpace(os.Getenv(postgresHostVariable)) == "" {
		problems = append(problems, fmt.Errorf("%s is required when %s=postgres", postgresHostVariable, mode.EnvStorageMode))
	}
	if err := errors.Join(problems...); err != nil {
		return StorageBackend{}, err
	}
	return StorageBackend{Mode: mode.Postgres, InstanceID: instanceID}, nil
}

// IgnoredPostgresVariables names the PG* variables set while the store is SQLite, so an operator who set them hears they have no effect.
func IgnoredPostgresVariables() []string {
	var set []string
	for _, name := range postgresVariables {
		if _, present := os.LookupEnv(name); present {
			set = append(set, name)
		}
	}
	return set
}
