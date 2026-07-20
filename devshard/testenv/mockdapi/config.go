package mockdapi

import (
	"time"

	cosrv "devshard/chainoracle/server"
)

// Config wires mock-dapi listeners and upstream mock-chain.
type Config struct {
	GRPCAddr          string
	HTTPAddr          string
	ChainGRPCAddr     string
	ChainRPCAddr      string
	ChainTestenvURL   string
	MLEndpoint        string
	ChainPollInterval time.Duration
	BlockInterval     time.Duration
	ChainID           string
	Versions          []cosrv.Version
	// BlockSeed seeds the mock block observer (deterministic headers).
	BlockSeed int64
	// GatewayBlockHeight / GatewayEpochIndex feed devshardctl public-API stubs.
	GatewayBlockHeight int64
	GatewayEpochIndex  uint64
}

// DefaultConfig returns listen defaults for local dev.
func DefaultConfig() Config {
	return Config{
		GRPCAddr:          ":9400",
		HTTPAddr:          ":9100",
		ChainGRPCAddr:     "127.0.0.1:9090",
		ChainRPCAddr:      "http://127.0.0.1:26657",
		MLEndpoint:        "http://mock-openai:8088",
		ChainPollInterval: time.Second,
		BlockInterval:     time.Second,
		ChainID:           "gonka-test",
		BlockSeed:         42,
		Versions: []cosrv.Version{
			{Name: "v2", Binary: "file:///opt/devshard/devshardd", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}
}
