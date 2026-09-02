package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MultiConfigOpts customizes WriteMultiConfig skeletons.
// ValidationRate 0 omits the field so gencompose/ApplyDefaults use the
// testenv default (6000 bps). Pass a non-zero value (e.g. 10000) per scenario.
type MultiConfigOpts struct {
	Hosts          int
	EscrowSlots    int
	MaxNonce       uint32
	EscrowAmount   uint64
	ValidationRate uint32 // 0 = default; else params + seed escrow snapshot
}

// WriteStackConfig writes the standard two-versiond stack config.
// Service ports are selected at runtime and published on Docker-assigned
// localhost ports, so citest can coexist with local-test-net and dev stacks.
// It uses the default validation_rate rather than the lease-race 100% rate.
func WriteStackConfig(t *testing.T, dir string) {
	t.Helper()
	WriteMultiConfig(t, dir, MultiConfigOpts{Hosts: 2, EscrowSlots: 2})
}

// WriteValidationLeaseRaceConfig writes a three-host validation lease config:
// hosts[0]+hosts[1] = HA same KEY_NAME; hosts[2] = solo executor participant.
// Seeds validation_rate=10000 (100%) for dense lease races.
func WriteValidationLeaseRaceConfig(t *testing.T, dir string) {
	t.Helper()
	WriteMultiConfig(t, dir, MultiConfigOpts{
		Hosts:          3,
		EscrowSlots:    4,
		ValidationRate: 10000,
	})
}

// WriteMultiConfig writes a multi-versiond citest skeleton with the given opts.
func WriteMultiConfig(t *testing.T, dir string, opts MultiConfigOpts) {
	t.Helper()
	if opts.Hosts < 2 {
		opts.Hosts = 2
	}
	if opts.EscrowSlots <= 0 {
		opts.EscrowSlots = opts.Hosts
	}

	chainGRPC := pickFreePort(t)
	chainRPC := pickFreePort(t)
	chainTestenv := pickFreePort(t)
	dapiGRPC := pickFreePort(t)
	dapiHTTP := pickFreePort(t)
	openAIHTTP := pickFreePort(t)
	routerPort := pickFreePort(t)
	gatewayPort := pickFreePort(t)

	paramsRate := ""
	escrowRate := ""
	paramsMaxNonce := ""
	escrowAmount := ""
	if opts.MaxNonce > 0 {
		paramsMaxNonce = fmt.Sprintf("\n  max_nonce: %d", opts.MaxNonce)
	}
	if opts.EscrowAmount > 0 {
		escrowAmount = fmt.Sprintf("\n    amount: %d", opts.EscrowAmount)
	}
	if opts.ValidationRate > 0 {
		paramsRate = fmt.Sprintf("\n  validation_rate: %d", opts.ValidationRate)
		escrowRate = fmt.Sprintf("\n    validation_rate: %d", opts.ValidationRate)
	}

	var hosts strings.Builder
	for i := 0; i < opts.Hosts; i++ {
		hosts.WriteString(fmt.Sprintf("  - id: versiond-%d\n    private_key_hex: TODO\n", i))
	}

	skeleton := fmt.Sprintf(`chain_id: gonka-test
block_height: 150
epoch:
  index: 1
  poc_start_block_height: 100
  epoch_length: 400
params:
  devshard_requests_enabled: true%s%s
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
  slots: %d
hosts:
%suser:
  private_key_hex: TODO
warm_grantee:
  private_key_hex: TODO
escrows:
  - id: 1
    model_id: test-model%s%s
grantees:
  - granter_address: ""
    message_type_url: /inference.inference.MsgStartInference
    grantees: [""]
`, paramsMaxNonce, paramsRate, chainGRPC, chainRPC, chainTestenv, dapiGRPC, dapiHTTP, openAIHTTP, routerPort, gatewayPort, opts.EscrowSlots, hosts.String(), escrowAmount, escrowRate)
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
