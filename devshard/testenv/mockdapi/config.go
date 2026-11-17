package mockdapi

import (
	"fmt"
	"strings"
	"time"

	"devshard/chainoracle/params"
	cosrv "devshard/chainoracle/server"
	"devshard/testenv/gatewayphase"
)

// Config wires mock-dapi listeners and upstream mock-chain.
type Config struct {
	GRPCAddr          string
	HTTPAddr          string
	ChainGRPCAddr     string
	ChainRPCAddr      string
	ChainTestenvURL   string
	MLEndpoint        string // MOCK_ML_ENDPOINT single-node shorthand
	MLNodes           []params.MLNode
	ChainPollInterval time.Duration
	BlockInterval     time.Duration
	ChainID           string
	Versions          []cosrv.Version
	BinaryDir         string
	// BlockSeed seeds the mock block observer (deterministic headers).
	BlockSeed int64
	// GatewayBlockHeight / GatewayEpochIndex pin the devshardctl public-API
	// stubs; both zero makes them follow mock-chain block height and epoch.
	GatewayBlockHeight int64
	GatewayEpochIndex  uint64
	// OmitBlockRoutes emulates 0.2.15 / pre-#1738 dapi: no HTTP /block/* and
	// GetBlockHeader / ProveBlockPath answer Unimplemented so host failover
	// takes the 15-minute skip. /healthz and /versions remain so versiond
	// still boots.
	OmitBlockRoutes bool
	// MLNodeHosts binds node IDs to host IDs and HostAddrs maps gRPC peer IPs
	// or DNS names to host IDs. A mapped caller draws only from its host's
	// nodes; the rest share the unbound nodes. Both empty keeps the global pool.
	MLNodeHosts map[string]string
	HostAddrs   map[string]string
	// PhaseSchedule is the gateway phase timeline from mock-dapi start;
	// POST /testenv/phase replaces it at runtime.
	PhaseSchedule []gatewayphase.Step
}

// MLNode is one Mock ML endpoint exposed through AcquireMLNode.
type MLNode = params.MLNode

// DefaultConfig returns listen defaults for local dev.
func DefaultConfig() Config {
	return Config{
		GRPCAddr:          ":9400",
		HTTPAddr:          ":9100",
		ChainGRPCAddr:     "127.0.0.1:9090",
		ChainRPCAddr:      "http://127.0.0.1:26657",
		MLEndpoint:        "http://mock-openai-0:8088",
		ChainPollInterval: time.Second,
		BlockInterval:     time.Second,
		ChainID:           "gonka-test",
		BlockSeed:         42,
		Versions: []cosrv.Version{
			{Name: "v2", Binary: "file:///opt/devshard/devshardd", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}
}

// ParsePairsEnv parses "key=value,key=value" (MOCK_ML_NODE_HOSTS,
// MOCK_DAPI_HOST_ADDRS). Empty input yields an empty map.
func ParsePairsEnv(raw string) (map[string]string, error) {
	out := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("invalid entry %q (want key=value)", part)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		out[key] = value
	}
	return out, nil
}
