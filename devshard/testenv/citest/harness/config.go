package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// WriteS1Config writes a minimal multi-versiond (2 hosts) config skeleton for Phase 8 S1.
// Service listen ports are chosen at runtime; Docker assigns localhost host ports later.
func WriteS1Config(t *testing.T, dir string) {
	t.Helper()
	chainGRPC := pickFreePort(t)
	chainRPC := pickFreePort(t)
	chainTestenv := pickFreePort(t)
	dapiGRPC := pickFreePort(t)
	dapiHTTP := pickFreePort(t)
	openAIHTTP := pickFreePort(t)
	routerPort := pickFreePort(t)
	gatewayPort := pickFreePort(t)

	skeleton := fmt.Sprintf(`chain_id: gonka-test
block_height: 150
epoch:
  index: 1
  poc_start_block_height: 100
  epoch_length: 400
params:
  devshard_requests_enabled: true
mock_chain:
  grpc_port: %d
  rpc_port: %d
  testenv_port: %d
mock_dapi:
  grpc_port: %d
  http_port: %d
mock_openai:
  http_port: %d
versiond:
  mode: multi
  version_name: v2
  binary_version: 0.2.13-v2-r2
versiond_router:
  port: %d
devshardctl:
  port: %d
postgres:
  enabled: true
network:
  subnet: 172.31.0.0/24
  base_ip: 172.31.0
escrow:
  slots: 2
hosts:
  - id: versiond-0
    private_key_hex: TODO
  - id: versiond-1
    private_key_hex: TODO
user:
  private_key_hex: TODO
warm_grantee:
  private_key_hex: TODO
escrows:
  - id: 1
    model_id: test-model
grantees:
  - granter_address: ""
    message_type_url: /inference.inference.MsgStartInference
    grantees: [""]
`, chainGRPC, chainRPC, chainTestenv, dapiGRPC, dapiHTTP, openAIHTTP, routerPort, gatewayPort)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(skeleton), 0o644))
}

// WriteSingleVersiondConfig writes a single-host config for gateway smoke (Phase 7).
func WriteSingleVersiondConfig(t *testing.T, dir string) {
	t.Helper()
	skeleton := strings.TrimPrefix(`chain_id: gonka-test
block_height: 150
epoch:
  index: 1
  poc_start_block_height: 100
params:
  devshard_requests_enabled: true
versiond:
  mode: single
  version_name: v2
  binary_version: 0.2.13-v2-r2
postgres:
  enabled: false
escrow:
  slots: 1
hosts:
  - id: versiond-0
    private_key_hex: TODO
user:
  private_key_hex: TODO
warm_grantee:
  private_key_hex: TODO
escrows:
  - id: 1
    model_id: test-model
grantees:
  - granter_address: ""
    message_type_url: /inference.inference.MsgStartInference
    grantees: [""]
`, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(skeleton), 0o644))
}
