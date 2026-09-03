//go:build testenvci

package citest

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cosrv "devshard/chainoracle/server"
	"devshard/testenv/citest/harness"
)

// TestDynamicCatalogRemovalAndReadmission verifies that the router and real
// versiond supervisors consume one non-empty desired set with the same removal
// semantics. Re-admission must satisfy the configured two-host reserve again.
func TestDynamicCatalogRemovalAndReadmission(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack := harness.NewStack(t, "citest-dynamic-catalog-lifecycle-*")
	harness.RequireLinuxDevshardd(t, stack.TestenvDir)
	harness.WriteStackConfig(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	require.Len(t, cfg.Hosts, 2)

	// The oracle, rather than VERSIOND_FORCE, must own the desired-set removal.
	// Route withdrawal is maintenance-only until the source carries a monotonic
	// revision, so this destructive lifecycle test opts into that contract.
	harness.PatchComposeRemoveEnvKey(t, stack.ComposePath, "VERSIOND_FORCE")
	harness.PatchComposeInsertEnvAfter(t, stack.ComposePath,
		"VERSIOND_ROUTING_CATALOG_POLL_SECONDS",
		`VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS: "true"`)
	stack.Up(t)
	eps := stack.Endpoints(t, cfg)
	client := harness.HTTPClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-router", "mock-dapi")
		}
	})

	version := cfg.Versiond.VersionName
	versionHealth := eps.RouterHTTP + "/" + version + "/healthz"
	harness.WaitGETOK(t, client, versionHealth, 5*time.Minute, "initial dynamic route", stack)
	adminReady := stack.ComposeExec(t, "versiond-router", "wget", "-qO-",
		"http://127.0.0.1:8404/readyz?version="+version)
	require.Equal(t, "ready", strings.TrimSpace(adminReady),
		"router admin readiness must reflect the admitted dynamic backend")

	var initial cosrv.VersionConfig
	require.NoError(t, harness.GetJSON(client, eps.MockDapiHTTP+"/versions", &initial))
	require.Len(t, initial.Versions, 1)
	require.Equal(t, version, initial.Versions[0].Name)

	harness.Step(t, "remove %q with a non-empty desired set", version)
	pending := cosrv.Version{
		Name:   "v-pending",
		Binary: harness.VersiondBinaryURL(cfg.MockDapi.HTTPPort, "missing.zip"),
		SHA256: strings.Repeat("0", 64),
	}
	harness.PatchTestenvVersions(t, client, eps.MockDapiHTTP, []cosrv.Version{pending})
	require.True(t, harness.AssertEventually(t, 5*time.Minute, time.Second, func() bool {
		for _, host := range cfg.Hosts {
			entries, err := harness.TryVersiondHealth(stack, host.ID)
			if err != nil || harness.HasVersiondHealthEntry(entries, version, "", "") {
				return false
			}
		}
		return true
	}), "versiond children for %q were not retired", version)
	require.Equal(t, http.StatusServiceUnavailable,
		harness.WaitGETStatus(t, client, versionHealth, 2*time.Minute, "retired route", http.StatusServiceUnavailable))

	harness.Step(t, "re-add %q while only one versiond host is available", version)
	standby := cfg.Hosts[1].ID
	stack.StopService(t, standby)
	harness.PatchTestenvVersions(t, client, eps.MockDapiHTTP, initial.Versions)
	require.True(t, harness.AssertEventually(t, 5*time.Minute, time.Second, func() bool {
		entries, err := harness.TryVersiondHealth(stack, cfg.Hosts[0].ID)
		return err == nil && harness.HasVersiondHealthEntry(entries, version, "running", "")
	}), "surviving host did not restart %q", version)
	require.Equal(t, http.StatusServiceUnavailable,
		harness.WaitGETStatus(t, client, versionHealth, 30*time.Second,
			"route held behind two-host activation reserve", http.StatusServiceUnavailable))

	harness.Step(t, "restore the second host and satisfy the activation reserve")
	stack.StartService(t, standby)
	harness.WaitGETOK(t, client, versionHealth, 5*time.Minute, "re-admitted dynamic route", stack)
	backend := harness.RequireResponseHeader(t, client,
		harness.RouterSessionURL(eps.RouterHTTP, version, "citest-catalog-readmission", "/healthz"),
		versiondBackendHeader)
	require.True(t, strings.HasPrefix(backend, backendDynamicPrefix),
		"re-admitted route backend = %q, want dynamic pool", backend)
}
