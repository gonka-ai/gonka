package main

// Config holds runtime parameters for subnetd.
// Population is intentionally left to the caller (main).
// Full config loading (YAML, flags, env) will be added later.
type Config struct {
	HTTPAddr        string // HTTP listen address, e.g. ":18080"
	ChainGRPCURL    string // chain gRPC endpoint, e.g. "localhost:9090"
	ChainRPCURL     string // CometBFT RPC HTTP endpoint for WebSocket events, e.g. "http://localhost:26657"
	NodeManagerAddr string // NodeManager gRPC address for ML node acquisition
	SQLitePath      string // path to SQLite session state DB
	PostgresDSN     string // PostgreSQL DSN for payload + claim stores
	SignerKeyName   string // key name in the cosmos keyring
	KeyringBackend  string // "test" or "file"
	KeyringDir      string // keyring root directory
	ChainID         string // chain ID for tx signing, e.g. "inference-1"
	InstanceAddress string // this node's address; used as claim identity for validation
	PublicURL       string // publicly reachable base URL for this node
}
