package citest

import (
	"path/filepath"
	"strings"
	"testing"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// joinComposeHostEnv is the minimum .env needed to render deploy/join.
var joinComposeHostEnv = map[string]string{
	"KEY_NAME":                   "citest",
	"ACCOUNT_PUBKEY":             "pubkey",
	"KEYRING_PASSWORD":           "keyring-pass",
	"POSTGRES_HOST":              "postgres",
	"POSTGRES_DB":                "payloads",
	"POSTGRES_USER":              "payloads",
	"POSTGRES_PASSWORD":          "payload-secret",
	"DEVSHARD_POSTGRES_PASSWORD": "session-secret",
}

func joinCompose(t *testing.T, overlays ...string) harness.ComposeServiceEnv {
	t.Helper()
	dir := filepath.Join(harness.RepoRoot(t), "deploy", "join")
	files := append([]string{"docker-compose.yml"}, overlays...)
	return harness.ComposeEnvConfig(t, dir, joinComposeHostEnv, files...)
}

// requireSessionDBComplete asserts compose supplies every session-DB setting,
// since common/devshardobs has no built-in values and refuses to fall back to
// libpq PG* (which is dapi's payload database).
func requireSessionDBComplete(t *testing.T, service string, env map[string]string) {
	t.Helper()
	if env["DEVSHARD_OBS_DATABASE_URL"] != "" {
		return
	}
	for _, key := range []string{
		"DEVSHARD_OBS_PGPORT",
		"DEVSHARD_OBS_PGDATABASE",
		"DEVSHARD_OBS_PGUSER",
	} {
		require.NotEmptyf(t, env[key], "%s sets DEVSHARD_OBS_PGHOST, so %s is required too", service, key)
	}
}

// requireSessionDBSeparateFromPayload asserts the obs session lookup is
// configured against its own database, never dapi's payload storage.
func requireSessionDBSeparateFromPayload(t *testing.T, service string, env map[string]string) {
	t.Helper()
	require.Equal(t, "devshard-postgres", env["DEVSHARD_OBS_PGHOST"], "%s session host", service)
	require.Equal(t, "devshardd", env["DEVSHARD_OBS_PGDATABASE"], "%s session database", service)
	require.Equal(t, "session-secret", env["DEVSHARD_OBS_PGPASSWORD"], "%s session password", service)
	requireSessionDBComplete(t, service, env)
	if payloadDB := env["PGDATABASE"]; payloadDB != "" {
		require.NotEqual(t, payloadDB, env["DEVSHARD_OBS_PGDATABASE"],
			"%s must not run session lookup against the payload database", service)
	}
	if payloadPass := env["PGPASSWORD"]; payloadPass != "" {
		require.NotEqual(t, payloadPass, env["DEVSHARD_OBS_PGPASSWORD"],
			"%s must not reuse payload credentials for the session DB", service)
	}
}

// TestJoinCompose_DefaultsLeaveSessionLookupUnconfigured pins the base join
// stack: dapi gets payload PG* only. PGDATABASE=payloads with an empty PGHOST
// must not read as a session DB — common/devshardobs then serves versionless
// obs fan-out instead of failing init and falling through to 410 Gone.
func TestJoinCompose_DefaultsLeaveSessionLookupUnconfigured(t *testing.T) {
	cfg := joinCompose(t)

	api := cfg.Env(t, "api")
	require.Equal(t, "payloads", api["PGDATABASE"], "payload database unchanged")
	require.Empty(t, api["DEVSHARD_OBS_PGHOST"], "no session host by default")
	require.Empty(t, api["DEVSHARD_OBS_DATABASE_URL"], "no session URL by default")
	require.Empty(t, api["DEVSHARD_VERSIOND_URL"], "handler default http://versiond:8080 applies")

	edge := cfg.Env(t, "edge-api")
	require.Empty(t, edge["DEVSHARD_OBS_PGHOST"])
	require.Empty(t, edge["DEVSHARD_OBS_DATABASE_URL"])
}

// TestJoinCompose_PayloadPostgresIsNotSessionDB layers the payload Postgres
// overlay: a fully configured payload DB still leaves session lookup off.
func TestJoinCompose_PayloadPostgresIsNotSessionDB(t *testing.T) {
	cfg := joinCompose(t, "docker-compose.postgres.yml")

	api := cfg.Env(t, "api")
	require.Equal(t, "payloads", api["PGDATABASE"])
	require.NotEmpty(t, api["PGHOST"], "payload storage is wired")
	require.Empty(t, api["DEVSHARD_OBS_PGHOST"], "payload host must not double as the session host")
	require.Empty(t, api["DEVSHARD_OBS_PGDATABASE"])
}

// TestJoinCompose_VersiondOverlayWiresRouterAndSessionDB covers the HA case:
// versionless obs must follow the sticky router and the session database.
func TestJoinCompose_VersiondOverlayWiresRouterAndSessionDB(t *testing.T) {
	cfg := joinCompose(t, "docker-compose.postgres.yml", "docker-compose.versiond.yml")

	for _, service := range []string{"api", "edge-api"} {
		t.Run(service, func(t *testing.T) {
			env := cfg.Env(t, service)
			require.Equal(t, "http://versiond-router:8080", env["DEVSHARD_VERSIOND_URL"],
				"obs must fan out through the router, not a pinned versiond")
			requireSessionDBSeparateFromPayload(t, service, env)
			require.NotEqual(t, env["PGHOST"], env["DEVSHARD_OBS_PGHOST"],
				"payload and session databases live on different hosts here")
		})
	}

	proxy := cfg.Env(t, "proxy")
	require.Equal(t, "versiond-router", proxy["VERSIOND_SERVICE_NAME"])

	versiond := cfg.Env(t, "versiond")
	require.Equal(t, "devshard-postgres", versiond["PGHOST"], "versiond owns the session DB directly")
	require.Equal(t, "devshardd", versiond["PGDATABASE"])
}

// TestJoinCompose_MultiEdgeWithoutObsOverlayDegradesSafely documents the
// combination the obs-ha overlay exists for: without it, edge-api2/3 keep the
// defaults (no session DB), which is fan-out only rather than a broken lookup.
func TestJoinCompose_MultiEdgeWithoutObsOverlayDegradesSafely(t *testing.T) {
	cfg := joinCompose(t,
		"docker-compose.postgres.yml",
		"docker-compose.versiond.yml",
		"docker-compose.edge-api-multi.yml",
	)

	for _, service := range []string{"edge-api2", "edge-api3"} {
		env := cfg.Env(t, service)
		require.Empty(t, env["DEVSHARD_OBS_PGHOST"], "%s: unset session host, not the payload host", service)
		require.NotEqual(t, "payloads", env["DEVSHARD_OBS_PGDATABASE"], "%s", service)
	}
}

// TestCompose_SessionDBEnvIsAlwaysComplete sweeps every service of every
// supported overlay combination: whichever compose file turns the lookup on
// must supply the whole session-DB config, or the service silently runs
// fan-out-only after logging the missing variable.
func TestCompose_SessionDBEnvIsAlwaysComplete(t *testing.T) {
	joinCombos := [][]string{
		{},
		{"docker-compose.postgres.yml"},
		{"docker-compose.postgres.yml", "docker-compose.versiond.yml"},
		{"docker-compose.postgres.yml", "docker-compose.versiond.yml", "docker-compose.edge-api-multi.yml"},
		{
			"docker-compose.postgres.yml", "docker-compose.versiond.yml",
			"docker-compose.edge-api-multi.yml", "docker-compose.edge-api-obs-ha.yml",
		},
	}
	for _, overlays := range joinCombos {
		t.Run("join "+strings.Join(overlays, "+"), func(t *testing.T) {
			requireEverySessionDBComplete(t, joinCompose(t, overlays...))
		})
	}

	ltnDir := filepath.Join(harness.RepoRoot(t), "local-test-net")
	ltnEnv := map[string]string{"KEY_NAME": "genesis", "EDGE_API_BUILD_CONTEXT": ".."}
	ltnCombos := [][]string{
		{"docker-compose-base.yml", "docker-compose.genesis.yml"},
		{
			"docker-compose-base.yml", "docker-compose.genesis.yml",
			"docker-compose.versiond.yml", "docker-compose.devshard-postgres.yml",
		},
		{
			"docker-compose-base.yml", "docker-compose.genesis.yml",
			"docker-compose.versiond.yml", "docker-compose.devshard-postgres.yml",
			"docker-compose.devshard-router-proxy.yml", "docker-compose.edge-api.yml",
			"docker-compose.devshard-obs-ha.yml",
		},
	}
	for _, files := range ltnCombos {
		t.Run("local-test-net "+strings.Join(files[2:], "+"), func(t *testing.T) {
			requireEverySessionDBComplete(t, harness.ComposeEnvConfig(t, ltnDir, ltnEnv, files...))
		})
	}
}

func requireEverySessionDBComplete(t *testing.T, cfg harness.ComposeServiceEnv) {
	t.Helper()
	for service, env := range cfg {
		if env["DEVSHARD_OBS_PGHOST"] == "" {
			continue
		}
		requireSessionDBComplete(t, service, env)
	}
}

// TestLocalTestNetCompose_ObsHAOverlayCoversEveryEdgeAPI is the local-test-net
// equivalent: with the obs-ha overlay layered last, api/edge-api/edge-api-2/3 all
// fan out through versiond-router against the session DB, while dapi keeps its
// own payload database.
func TestLocalTestNetCompose_ObsHAOverlayCoversEveryEdgeAPI(t *testing.T) {
	dir := filepath.Join(harness.RepoRoot(t), "local-test-net")
	env := map[string]string{"KEY_NAME": "genesis", "EDGE_API_BUILD_CONTEXT": ".."}
	cfg := harness.ComposeEnvConfig(t, dir, env,
		"docker-compose-base.yml",
		"docker-compose.versiond.yml",
		"docker-compose.devshard-postgres.yml",
		"docker-compose.devshard-router-proxy.yml",
		"docker-compose.edge-api.yml",
		"docker-compose.devshard-obs-ha.yml",
	)

	for _, service := range []string{"api", "edge-api", "edge-api-2", "edge-api-3"} {
		t.Run(service, func(t *testing.T) {
			svc := cfg.Env(t, service)
			require.Equal(t, "http://genesis-versiond-router:8080", svc["DEVSHARD_VERSIOND_URL"])
			require.Equal(t, "devshard-postgres", svc["DEVSHARD_OBS_PGHOST"])
			require.Equal(t, "devshardd", svc["DEVSHARD_OBS_PGDATABASE"])
			requireSessionDBComplete(t, service, svc)
			if payloadDB := svc["PGDATABASE"]; payloadDB != "" {
				require.NotEqual(t, payloadDB, svc["DEVSHARD_OBS_PGDATABASE"])
			}
		})
	}

	api := cfg.Env(t, "api")
	require.Equal(t, "payloads", api["PGDATABASE"], "dapi payload storage untouched")
	require.NotEqual(t, api["PGHOST"], api["DEVSHARD_OBS_PGHOST"])
}

// TestJoinCompose_ObsHAOverlayCoversEveryEdgeAPI asserts the full HA layering
// gives every edge-api replica the router URL and the session DB.
func TestJoinCompose_ObsHAOverlayCoversEveryEdgeAPI(t *testing.T) {
	cfg := joinCompose(t,
		"docker-compose.postgres.yml",
		"docker-compose.versiond.yml",
		"docker-compose.edge-api-multi.yml",
		"docker-compose.edge-api-obs-ha.yml",
	)

	for _, service := range []string{"api", "edge-api", "edge-api2", "edge-api3"} {
		t.Run(service, func(t *testing.T) {
			env := cfg.Env(t, service)
			require.Equal(t, "http://versiond-router:8080", env["DEVSHARD_VERSIOND_URL"])
			requireSessionDBSeparateFromPayload(t, service, env)
		})
	}
}
