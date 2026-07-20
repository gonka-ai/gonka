package harness

import (
	"testing"

	"devshard/testenv/config"
)

// BootMockChainStack renders citest compose and starts only mock-chain (gRPC transport tests).
func BootMockChainStack(t *testing.T, prefix string) (*Stack, *config.File, Endpoints) {
	t.Helper()
	stack := NewStack(t, prefix)
	WriteS1Config(t, stack.WorkDir)
	stack.RunGencompose(t)
	cfg := stack.LoadConfig(t)
	stack.UpServices(t, false, "mock-chain")
	return stack, cfg, EndpointsFromConfig(cfg)
}
