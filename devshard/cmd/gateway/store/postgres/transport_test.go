package postgres

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func clearPostgresEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGOPTIONS", "PGSSLROOTCERT"} {
		t.Setenv(name, "")
	}
}

// Test flow:
//  1. Build the connection config for URLs that differ in host and sslmode, including a plaintext fallback.
//  2. Assert a remote host without a verified-or-required TLS mode is refused with ErrPlaintextRemoteHost.
//  3. Assert require, verify-ca and verify-full on a remote host, and plaintext on loopback, localhost and a unix socket, are accepted.
func TestTLSIsRequiredForRemoteHostsOnTheConfigPgxWillDial(t *testing.T) {
	clearPostgresEnvironment(t)
	for _, testCase := range []struct {
		name       string
		connection string
		wantRefuse bool
	}{
		{"remote, default sslmode falls back to plaintext", "postgres://u:p@db.example.com/gateway", true},
		{"remote, prefer", "postgres://u:p@db.example.com/gateway?sslmode=prefer", true},
		{"remote, allow", "postgres://u:p@db.example.com/gateway?sslmode=allow", true},
		{"remote, disable", "postgres://u:p@db.example.com/gateway?sslmode=disable", true},
		{"remote, require", "postgres://u:p@db.example.com/gateway?sslmode=require", false},
		{"remote, verify-ca", "postgres://u:p@db.example.com/gateway?sslmode=verify-ca", false},
		{"remote, verify-full", "postgres://u:p@db.example.com/gateway?sslmode=verify-full", false},
		{"loopback address, disable", "postgres://u:p@127.0.0.1/gateway?sslmode=disable", false},
		{"localhost, disable", "postgres://u:p@localhost/gateway?sslmode=disable", false},
		{"unix socket", "host=/var/run/postgresql user=u dbname=gateway", false},
		{"second host remote, prefer", "host=127.0.0.1,db.example.com user=u dbname=gateway sslmode=prefer", true},
		{"both hosts remote, require", "host=db-a.example.com,db-b.example.com user=u dbname=gateway sslmode=require", false},
	} {
		_, err := connectionConfig(testCase.connection)
		if errors.Is(err, ErrPlaintextRemoteHost) != testCase.wantRefuse || (err != nil && !testCase.wantRefuse) {
			t.Errorf("%s: connectionConfig() = %v, want refusal %v", testCase.name, err, testCase.wantRefuse)
		}
	}
}

// Test flow:
//  1. Point PGHOST at loopback and PGSERVICE at a service file naming a remote host with sslmode=disable.
//  2. Build the connection config from the environment alone.
//  3. Assert the plaintext remote host is refused although the variables looked safe.
func TestAServiceFileCannotSlipAPlaintextRemoteHostPastTheCheck(t *testing.T) {
	clearPostgresEnvironment(t)
	serviceFile := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(serviceFile, []byte("[gateway]\nhost=db.example.com\nsslmode=disable\n"), 0o600); err != nil {
		t.Fatalf("writing the service file: %v", err)
	}
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGSERVICEFILE", serviceFile)
	t.Setenv("PGSERVICE", "gateway")
	if _, err := connectionConfig(""); !errors.Is(err, ErrPlaintextRemoteHost) {
		t.Fatalf("connectionConfig() = %v, want ErrPlaintextRemoteHost", err)
	}
}

// Test flow:
//  1. Build the connection config for a loopback URL.
//  2. Assert every connection asks for synchronous_commit=on and for a writable primary.
func TestEveryConnectionPinsDurableCommitsAndAWritablePrimary(t *testing.T) {
	clearPostgresEnvironment(t)
	cfg, err := connectionConfig("postgres://u:p@127.0.0.1/gateway?sslmode=disable")
	if err != nil {
		t.Fatalf("connectionConfig() = %v, want nil", err)
	}
	if got := cfg.ConnConfig.RuntimeParams[synchronousCommitParam]; got != synchronousCommitOn {
		t.Fatalf("synchronous_commit = %q, want %q", got, synchronousCommitOn)
	}
	if cfg.ConnConfig.ValidateConnect == nil {
		t.Fatal("ValidateConnect is unset, want the read-write check")
	}
}

// Test flow:
//  1. Table-driven: a serialization failure, a deadlock, a constraint violation and a plain error.
//  2. Assert only the first two are retryable.
func TestOnlyASerializationFailureOrADeadlockIsRetried(t *testing.T) {
	for _, testCase := range []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: serializationFailure}, true},
		{&pgconn.PgError{Code: deadlockDetected}, true},
		{&pgconn.PgError{Code: "23505"}, false},
		{errors.New("plain"), false},
	} {
		if got := isRetryable(testCase.err); got != testCase.want {
			t.Errorf("isRetryable(%v) = %v, want %v", testCase.err, got, testCase.want)
		}
	}
}
