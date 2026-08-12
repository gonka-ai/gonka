package sessionversion

import (
	"context"
	"testing"
)

func TestOpenFromEnvRejectsAmbiguousPostgresConfiguration(t *testing.T) {
	for _, variable := range []string{"PGHOST", "PGPORT", "PGSSLMODE"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://user:pass@database-a/db")
			t.Setenv(variable, "conflicting-value")

			if _, err := OpenFromEnv(context.Background()); err == nil {
				t.Fatalf("OpenFromEnv accepted DATABASE_URL and %s together", variable)
			}
		})
	}
}

func TestOpenFromEnvRejectsDatabaseURLInHA(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@database/db")
	t.Setenv("GONKA_HA", "true")

	if _, err := OpenFromEnv(context.Background()); err == nil {
		t.Fatal("OpenFromEnv accepted DATABASE_URL in HA")
	}
}
