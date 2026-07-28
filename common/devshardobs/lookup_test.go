package devshardobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// clearObsEnv resets every env var that can influence lookup wiring so each
// case states its own full configuration.
func clearObsEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DEVSHARD_OBS_DISABLE_SESSION_LOOKUP",
		"VERSIOND_DISABLE_SESSION_LOOKUP",
		"DEVSHARD_OBS_DATABASE_URL",
		"DEVSHARD_OBS_PGHOST",
		"DEVSHARD_OBS_PGPORT",
		"DEVSHARD_OBS_PGDATABASE",
		"DEVSHARD_OBS_PGUSER",
		"DEVSHARD_OBS_PGPASSWORD",
		"DATABASE_URL",
		"PGHOST",
		"PGPORT",
		"PGDATABASE",
		"PGUSER",
		"PGPASSWORD",
		"DEVSHARD_VERSIOND_URL",
		"VERSIOND_URL",
	} {
		t.Setenv(k, "")
	}
}

// versiondStub answers /healthz and version-pinned obs, so tests can exercise
// the full OpenFromEnv → handler path without a real versiond.
func versiondStub(t *testing.T, versions ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			entries := make([]map[string]string, 0, len(versions))
			for _, v := range versions {
				entries = append(entries, map[string]string{"name": v, "status": statusRunning})
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(entries); err != nil {
				t.Errorf("encode healthz: %v", err)
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/devshard/") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("versiond:" + r.URL.Path))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setPayloadPGEnv applies dapi's payload-storage libpq env. None of it may
// enable or influence the devshard session lookup.
func setPayloadPGEnv(t *testing.T, host string) {
	t.Helper()
	t.Setenv("PGHOST", host)
	t.Setenv("PGPORT", "5432")
	t.Setenv("PGDATABASE", "payloads")
	t.Setenv("PGUSER", "payloads")
	t.Setenv("PGPASSWORD", "payload-secret")
}

func TestPostgresConfigured(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{
			name: "join default: PGDATABASE=payloads with empty PGHOST",
			env:  map[string]string{"PGDATABASE": "payloads"},
			want: false,
		},
		{
			name: "dapi payload postgres fully configured",
			env: map[string]string{
				"PGHOST": "postgres", "PGDATABASE": "payloads",
				"PGUSER": "payloads", "PGPASSWORD": "secret",
			},
			want: false,
		},
		{
			name: "payload DATABASE_URL",
			env:  map[string]string{"DATABASE_URL": "postgres://payloads@postgres:5432/payloads"},
			want: false,
		},
		{
			name: "session host",
			env:  map[string]string{"DEVSHARD_OBS_PGHOST": "devshard-postgres"},
			want: true,
		},
		{
			name: "session URL",
			env:  map[string]string{"DEVSHARD_OBS_DATABASE_URL": "postgres://devshardd@devshard-postgres:5432/devshardd"},
			want: true,
		},
		{
			name: "session host wins alongside payload env",
			env: map[string]string{
				"PGHOST": "postgres", "PGDATABASE": "payloads",
				"DEVSHARD_OBS_PGHOST": "devshard-postgres",
			},
			want: true,
		},
		{
			name: "whitespace-only session host",
			env:  map[string]string{"DEVSHARD_OBS_PGHOST": "   "},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearObsEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := postgresConfigured(); got != tc.want {
				t.Fatalf("postgresConfigured()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestOpenLookupFromEnv_PayloadEnvNeverEnablesLookup(t *testing.T) {
	// Regression: PGDATABASE=payloads alone used to look "configured", causing a
	// doomed ping that aborted the whole obs router.
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"join default (empty PGHOST)", map[string]string{"PGDATABASE": "payloads"}},
		{"payload PG reachable-looking", map[string]string{
			"PGHOST": "postgres", "PGPORT": "5432", "PGDATABASE": "payloads",
			"PGUSER": "payloads", "PGPASSWORD": "secret",
		}},
		{"payload DATABASE_URL", map[string]string{
			"DATABASE_URL": "postgres://payloads:secret@postgres:5432/payloads",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearObsEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			lookup, err := OpenLookupFromEnv(ctx)
			if err != nil {
				t.Fatalf("payload env must not attempt a connection: %v", err)
			}
			if lookup != nil {
				t.Fatal("expected nil lookup (fan-out only)")
			}
		})
	}
}

func TestOpenLookupFromEnv_DisabledByFlag(t *testing.T) {
	for _, key := range []string{
		"DEVSHARD_OBS_DISABLE_SESSION_LOOKUP",
		"VERSIOND_DISABLE_SESSION_LOOKUP",
	} {
		t.Run(key, func(t *testing.T) {
			clearObsEnv(t)
			t.Setenv("DEVSHARD_OBS_PGHOST", "devshard-postgres")
			t.Setenv(key, "true")

			lookup, err := OpenLookupFromEnv(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if lookup != nil {
				t.Fatal("expected nil lookup when disabled")
			}
		})
	}
}

func TestLookupPoolConfig_UsesSessionDBNotPayloadDB(t *testing.T) {
	clearObsEnv(t)
	// Payload storage points at a different host AND database.
	setPayloadPGEnv(t, "postgres")
	// Session index lives on its own host/database/credentials.
	t.Setenv("DEVSHARD_OBS_PGHOST", "devshard-postgres")
	t.Setenv("DEVSHARD_OBS_PGPORT", "6543")
	t.Setenv("DEVSHARD_OBS_PGDATABASE", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGUSER", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGPASSWORD", "session-secret")

	cfg, err := lookupPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	conn := cfg.ConnConfig
	if conn.Host != "devshard-postgres" {
		t.Fatalf("host=%q want devshard-postgres (payload host must be ignored)", conn.Host)
	}
	if conn.Port != 6543 {
		t.Fatalf("port=%d want 6543", conn.Port)
	}
	if conn.Database != "devshardd" {
		t.Fatalf("database=%q want devshardd (payload db must be ignored)", conn.Database)
	}
	if conn.User != "devshardd" || conn.Password != "session-secret" {
		t.Fatalf("credentials=%q/%q want devshardd/session-secret", conn.User, conn.Password)
	}
}

// TestLookupPoolConfig_IncompleteSessionEnvIsAnError pins the env-only
// contract: nothing is defaulted in code, so a half-configured session DB is
// reported instead of silently resolving to payload PG* values.
func TestLookupPoolConfig_IncompleteSessionEnvIsAnError(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		missing string
	}{
		{
			name:    "host only",
			env:     map[string]string{"DEVSHARD_OBS_PGHOST": "devshard-postgres"},
			missing: "DEVSHARD_OBS_PGDATABASE",
		},
		{
			name: "no user",
			env: map[string]string{
				"DEVSHARD_OBS_PGHOST": "devshard-postgres", "DEVSHARD_OBS_PGPORT": "5432",
				"DEVSHARD_OBS_PGDATABASE": "devshardd",
			},
			missing: "DEVSHARD_OBS_PGUSER",
		},
		{
			name: "no port",
			env: map[string]string{
				"DEVSHARD_OBS_PGHOST":     "devshard-postgres",
				"DEVSHARD_OBS_PGDATABASE": "devshardd", "DEVSHARD_OBS_PGUSER": "devshardd",
			},
			missing: "DEVSHARD_OBS_PGPORT",
		},
		{
			name:    "URL without database or user",
			env:     map[string]string{"DEVSHARD_OBS_DATABASE_URL": "postgres://sessions-db:5432"},
			missing: "DEVSHARD_OBS_PGUSER",
		},
		{
			name: "URL with user but no database",
			env: map[string]string{
				"DEVSHARD_OBS_DATABASE_URL": "postgres://sess:pw@sessions-db:5432",
			},
			missing: "DEVSHARD_OBS_PGDATABASE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearObsEnv(t)
			setPayloadPGEnv(t, "postgres")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			cfg, err := lookupPoolConfig()
			if err == nil {
				t.Fatalf("expected error naming %s, got config for %s/%s as %s",
					tc.missing, cfg.ConnConfig.Host, cfg.ConnConfig.Database, cfg.ConnConfig.User)
			}
			if !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("error=%v want it to name %s", err, tc.missing)
			}
		})
	}
}

// TestLookupPoolConfig_PasswordlessSessionUserIgnoresPayloadPassword covers a
// trust-auth session DB: pgx would otherwise send dapi's payload password.
func TestLookupPoolConfig_PasswordlessSessionUserIgnoresPayloadPassword(t *testing.T) {
	clearObsEnv(t)
	setPayloadPGEnv(t, "postgres")
	t.Setenv("DEVSHARD_OBS_PGHOST", "devshard-postgres")
	t.Setenv("DEVSHARD_OBS_PGPORT", "5432")
	t.Setenv("DEVSHARD_OBS_PGDATABASE", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGUSER", "devshardd")

	cfg, err := lookupPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.Password; got != "" {
		t.Fatalf("password=%q want empty (payload password must not be inherited)", got)
	}
}

// TestLookupPoolConfig_DatabaseURLFillsGapsFromSessionEnv covers a URL that
// omits credentials: the gaps come from DEVSHARD_OBS_PG*, not payload PG*.
func TestLookupPoolConfig_DatabaseURLFillsGapsFromSessionEnv(t *testing.T) {
	clearObsEnv(t)
	setPayloadPGEnv(t, "postgres")
	t.Setenv("DEVSHARD_OBS_DATABASE_URL", "postgres://sessions-db:5432/sessions")
	t.Setenv("DEVSHARD_OBS_PGUSER", "sess")
	t.Setenv("DEVSHARD_OBS_PGPASSWORD", "session-secret")

	cfg, err := lookupPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	conn := cfg.ConnConfig
	if conn.Host != "sessions-db" || conn.Database != "sessions" {
		t.Fatalf("got %s/%s want sessions-db/sessions", conn.Host, conn.Database)
	}
	if conn.User != "sess" || conn.Password != "session-secret" {
		t.Fatalf("credentials=%q/%q want sess/session-secret", conn.User, conn.Password)
	}
}

// TestLookupPoolConfig_DatabaseURLUserWithoutPasswordIgnoresPayload guards the
// last inheritance path: pgx fills a missing password from libpq PGPASSWORD.
func TestLookupPoolConfig_DatabaseURLUserWithoutPasswordIgnoresPayload(t *testing.T) {
	clearObsEnv(t)
	setPayloadPGEnv(t, "postgres")
	t.Setenv("DEVSHARD_OBS_DATABASE_URL", "postgres://sess@sessions-db:5432/sessions")

	cfg, err := lookupPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.Password; got != "" {
		t.Fatalf("password=%q want empty (payload password must not be inherited)", got)
	}
}

func TestLookupPoolConfig_KeywordValueDSNKeepsItsOwnFields(t *testing.T) {
	clearObsEnv(t)
	setPayloadPGEnv(t, "postgres")
	t.Setenv("DEVSHARD_OBS_DATABASE_URL", "host=sessions-db port=5432 dbname=sessions user=sess password=pw")

	cfg, err := lookupPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	conn := cfg.ConnConfig
	if conn.Host != "sessions-db" || conn.Database != "sessions" || conn.User != "sess" || conn.Password != "pw" {
		t.Fatalf("got %s@%s/%s pw=%q want sess@sessions-db/sessions pw=pw",
			conn.User, conn.Host, conn.Database, conn.Password)
	}
}

func TestLookupPoolConfig_DatabaseURLTakesPrecedence(t *testing.T) {
	clearObsEnv(t)
	setPayloadPGEnv(t, "postgres")
	t.Setenv("DEVSHARD_OBS_DATABASE_URL", "postgres://sess:pw@sessions-db:7000/sessions")
	t.Setenv("DEVSHARD_OBS_PGHOST", "ignored-host")

	cfg, err := lookupPoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	conn := cfg.ConnConfig
	if conn.Host != "sessions-db" || conn.Port != 7000 || conn.Database != "sessions" {
		t.Fatalf("got %s:%d/%s want sessions-db:7000/sessions", conn.Host, conn.Port, conn.Database)
	}
}

func TestOpenFromEnv_JoinDefaultsBuildFanOutHandler(t *testing.T) {
	// Exact deploy/join docker-compose.yml api service defaults.
	clearObsEnv(t)
	t.Setenv("PGHOST", "")
	t.Setenv("PGPORT", "5432")
	t.Setenv("PGDATABASE", "payloads")
	t.Setenv("PGUSER", "payloads")
	t.Setenv("PGPASSWORD", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	router, err := OpenFromEnv(ctx)
	if err != nil {
		t.Fatalf("join defaults must not fail obs wiring: %v", err)
	}
	defer router.Close()

	if router.Handler == nil {
		t.Fatal("expected fan-out handler so dapi mounts obs instead of serving 410")
	}
	if router.Lookup != nil {
		t.Fatal("expected nil lookup on join defaults")
	}
}

// TestOpenFromEnv_UnreachableSessionDBServesFanOut is the "cannot connect"
// case: obs must come up and answer session-scoped requests over versiond
// instead of failing, and it must do so without stalling on Postgres.
func TestOpenFromEnv_UnreachableSessionDBServesFanOut(t *testing.T) {
	versiond := versiondStub(t, "v1", "v2")
	clearObsEnv(t)
	// Session DB configured but unreachable (nothing listens on port 1).
	t.Setenv("DEVSHARD_OBS_PGHOST", "127.0.0.1")
	t.Setenv("DEVSHARD_OBS_PGPORT", "1")
	t.Setenv("DEVSHARD_OBS_PGDATABASE", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGUSER", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGPASSWORD", "bad")
	t.Setenv("DEVSHARD_VERSIOND_URL", versiond.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	router, err := OpenFromEnv(ctx)
	if err != nil {
		t.Fatalf("lookup init failure must degrade, not abort: %v", err)
	}
	defer router.Close()
	if router.Handler == nil {
		t.Fatal("expected fan-out handler after lookup ping failure")
	}

	srv := httptest.NewServer(router.Handler)
	defer srv.Close()

	start := time.Now()
	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/escrow-1/mempool")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200 via fan-out", resp.StatusCode)
	}
	// The startup failure put the lookup on cooldown, so the request must not
	// pay another dial attempt.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("request took %s; expected fan-out without waiting on Postgres", elapsed)
	}
}

func TestOpenFromEnv_IncompleteSessionEnvDegradesToFanOut(t *testing.T) {
	clearObsEnv(t)
	setPayloadPGEnv(t, "postgres")
	// Host without database/user: unusable session config, and payload PG* is
	// not an acceptable substitute.
	t.Setenv("DEVSHARD_OBS_PGHOST", "devshard-postgres")

	router, err := OpenFromEnv(context.Background())
	if err != nil {
		t.Fatalf("incomplete session config must degrade, not abort: %v", err)
	}
	defer router.Close()

	if router.Handler == nil {
		t.Fatal("expected fan-out handler")
	}
	if router.Lookup != nil {
		t.Fatal("expected nil lookup for incomplete session config")
	}
}

// TestLookupCooldown_SkipsSessionDBThenRetries covers the recovery contract:
// while the session DB is down every call reports unavailability (the handler
// fans out), and once the cooldown lapses the lookup probes Postgres again.
func TestLookupCooldown_SkipsSessionDBThenRetries(t *testing.T) {
	clearObsEnv(t)
	t.Setenv("DEVSHARD_OBS_PGHOST", "127.0.0.1")
	t.Setenv("DEVSHARD_OBS_PGPORT", "1")
	t.Setenv("DEVSHARD_OBS_PGDATABASE", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGUSER", "devshardd")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lookup, err := OpenLookupFromEnv(ctx)
	if err != nil {
		t.Fatalf("unreachable session DB must not be an error: %v", err)
	}
	if lookup == nil {
		t.Fatal("expected a lookup that retries, not a permanent nil")
	}
	defer lookup.Close()

	if !lookup.inCooldown() {
		t.Fatal("startup ping failure must start a cooldown")
	}

	start := time.Now()
	_, ok, err := lookup.LookupSessionVersion(ctx, "escrow-1")
	if !errors.Is(err, errSessionDBUnavailable) {
		t.Fatalf("err=%v want errSessionDBUnavailable", err)
	}
	if ok {
		t.Fatal("no version can be resolved while the session DB is down")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cooldown call took %s; it must not dial Postgres", elapsed)
	}

	// Cooldown lapsed: the next call probes Postgres again (and fails, since
	// nothing is listening) rather than staying disabled forever.
	lookup.mu.Lock()
	lookup.unavailableTill = time.Now().Add(-time.Second)
	lookup.mu.Unlock()

	_, _, err = lookup.LookupSessionVersion(ctx, "escrow-1")
	if err == nil {
		t.Fatal("expected the retry to surface the connection failure")
	}
	if errors.Is(err, errSessionDBUnavailable) {
		t.Fatalf("retry must reach Postgres, got the skip marker: %v", err)
	}
	if !lookup.inCooldown() {
		t.Fatal("a failed retry must restart the cooldown")
	}
}

func TestOpenFromEnv_UsesConfiguredVersiondBase(t *testing.T) {
	clearObsEnv(t)
	t.Setenv("DEVSHARD_VERSIOND_URL", "http://versiond-router:8080")

	router, err := OpenFromEnv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()

	if got := router.Handler.versiond.String(); got != "http://versiond-router:8080" {
		t.Fatalf("versiond base=%q want http://versiond-router:8080", got)
	}
}

// TestHandlerNilLookupServesFanOut proves the degraded configuration still
// answers session-scoped obs by fanning out newest→oldest over versiond.
func TestHandlerNilLookupServesFanOut(t *testing.T) {
	var gotPaths []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if strings.Contains(r.URL.Path, "/v1/") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("mempool-from-v1"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer backend.Close()
	base, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	h, err := NewHandler(Config{
		VersiondBase: base,
		Lookup:       nil, // degraded: no session DB
		Versions:     StaticVersions([]string{"v1", "v2"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/escrow-1/mempool")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200 (fan-out should find v1)", resp.StatusCode)
	}
	if len(gotPaths) == 0 {
		t.Fatal("expected fan-out requests to versiond")
	}
	if gotPaths[0] != "/devshard/v2/sessions/escrow-1/mempool" {
		t.Fatalf("first fan-out=%q want newest version first", gotPaths[0])
	}
}

func TestBuildLibpqDSN(t *testing.T) {
	tests := []struct {
		name                             string
		host, port, database, user, pass string
		want                             string
	}{
		{
			name: "full", host: "devshard-postgres", port: "5432",
			database: "devshardd", user: "user", pass: "pass",
			want: "postgres://user:pass@devshard-postgres:5432/devshardd",
		},
		{
			name: "no credentials", host: "devshard-postgres", port: "5432",
			database: "devshardd",
			want:     "postgres://devshard-postgres:5432/devshardd",
		},
		{
			name: "user only", host: "db", port: "5432", database: "devshardd", user: "u",
			want: "postgres://u@db:5432/devshardd",
		},
		{
			name: "host already carries port", host: "db:6000", port: "5432", database: "devshardd",
			want: "postgres://db:6000/devshardd",
		},
		{
			name: "password needing escape", host: "db", port: "5432", database: "devshardd",
			user: "u", pass: "p@ss/word",
			want: "postgres://u:p%40ss%2Fword@db:5432/devshardd",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildLibpqDSN(tc.host, tc.port, tc.database, tc.user, tc.pass)
			if got != tc.want {
				t.Fatalf("dsn=%q want %q", got, tc.want)
			}
		})
	}
}
