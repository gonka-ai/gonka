//go:build testenvci

package citest

import (
	"testing"

	"devshard/testenv/citest/harness"
)

// TestStackSmoke brings up mock-chain, mock-dapi, versiond-router, two versiond
// hosts, and devshardctl; asserts each boundary is healthy.
func TestStackSmoke(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	harness.Step(t, "starting stack (2 versiond + router + gateway) — versiond child boot can take 1–3 min")
	stack, cfg, eps := harness.BootStack(t, "citest-stack-smoke-*")
	harness.WaitStackHealthy(t, stack, eps)

	harness.Step(t, "compose services running")
	stack.RequireServicesRunning(t,
		"mock-chain",
		"mock-dapi",
		"mock-openai-0",
		"versiond-router",
		"devshardctl",
		"devshard-postgres",
		cfg.Hosts[0].ID,
		cfg.Hosts[1].ID,
	)
}
