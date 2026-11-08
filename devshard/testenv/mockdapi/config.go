package mockdapi

import (
	"time"

	"devshard/chainoracle/params"
	cosrv "devshard/chainoracle/server"
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
	Versions          []cosrv.Version
	BinaryDir         string
	// GatewayBlockHeight / GatewayEpochIndex feed devshardctl public-API stubs.
	GatewayBlockHeight int64
	GatewayEpochIndex  uint64
	// OmitBlockRoutes emulates 0.2.15 / pre-#1738 dapi: no HTTP /block/* and
	// GetBlockHeader / ProveBlockPath answer Unimplemented so host failover
	// takes the 15-minute skip. /healthz and /versions remain so versiond
	// still boots.
	OmitBlockRoutes bool
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
		Versions: []cosrv.Version{
			{Name: "v2", Binary: "file:///opt/devshard/devshardd", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}
}
