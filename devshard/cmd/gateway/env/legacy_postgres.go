package env

import "os"

var legacyPostgresVariables = []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PG_CONNECT_TIMEOUT"}

// LegacyPostgresVariablesSet names devshardctl's Postgres variables present in the environment; the gateway reads none of them. See ../docs/operations.md, "One process per escrow set".
func LegacyPostgresVariablesSet() []string {
	var set []string
	for _, name := range legacyPostgresVariables {
		if _, present := os.LookupEnv(name); present {
			set = append(set, name)
		}
	}
	return set
}
