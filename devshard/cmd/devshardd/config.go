package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"devshard/cmd/devshardd/session"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/app"
)

var sdkConfigOnce sync.Once

type runtimeConfig struct {
	Port                    int
	DataDir                 string
	BinaryLogVersion        string
	RuntimeVersion          string
	ProtocolVersion         string
	NodeManagerAddr         string
	ValidationRetryInterval time.Duration
	ValidationLeaseTTL      time.Duration
	Node                    ChainNodeConfig
}

// ChainNodeConfig holds the chain connectivity and signing identity settings
// for devshardd. Inlined from decentralized-api/apiconfig to remove that
// dependency.
type ChainNodeConfig struct {
	ChainRpcUrl         string // Tendermint RPC URL (e.g. http://node:26657)
	ChainGrpcUrl        string // gRPC URL for chain.Client (e.g. node:9090)
	ChainID             string // chain-id for tx signing (e.g. gonka-mainnet)
	SignerKeyName       string
	KeyringBackend      string
	KeyringDir          string
	KeyringPassword     string
	AccountPubKeyBase64 string // base64-encoded cold account pubkey; falls back to signer pubkey if empty
}

// ApiAccount holds the account and signer keys for devshardd.
// Inlined from decentralized-api/apiconfig to remove that dependency.
// Only the fields and methods actually used by devshardd are included.
type ApiAccount struct {
	AccountKey    cryptotypes.PubKey
	SignerRecord  *keyring.Record
	AddressPrefix string
}

// AccountAddressBech32 returns the bech32-encoded account address.
func (a *ApiAccount) AccountAddressBech32() (string, error) {
	addr, err := sdk.Bech32ifyAddressBytes(a.AddressPrefix, a.AccountKey.Address())
	if err != nil {
		return "", fmt.Errorf("failed to Bech32-encode address: %w", err)
	}
	return addr, nil
}

// SignerAddressBech32 returns the bech32-encoded signer address.
func (a *ApiAccount) SignerAddressBech32() (string, error) {
	pubKey, err := a.SignerRecord.GetPubKey()
	if err != nil {
		return "", fmt.Errorf("failed to get signer public key: %w", err)
	}
	addr, err := sdk.Bech32ifyAddressBytes(a.AddressPrefix, pubKey.Address())
	if err != nil {
		return "", fmt.Errorf("failed to Bech32-encode address: %w", err)
	}
	return addr, nil
}

// initSdkBech32Prefix sets the Cosmos SDK global address prefixes so
// sdk.AccAddressFromBech32 accepts gonka1... addresses. Must be called before
// any address parsing.
//
// devshardd intentionally does not set CoinType: it opens existing keyring
// records and does not derive new keys from mnemonics.
func initSdkBech32Prefix() {
	sdkConfigOnce.Do(func() {
		cfg := sdk.GetConfig()
		cfg.SetBech32PrefixForAccount(app.AccountAddressPrefix, app.AccountAddressPrefix+"pub")
		cfg.SetBech32PrefixForValidator(app.AccountAddressPrefix+"valoper", app.AccountAddressPrefix+"valoperpub")
		cfg.SetBech32PrefixForConsensusNode(app.AccountAddressPrefix+"valcons", app.AccountAddressPrefix+"valconspub")
		cfg.Seal()
	})
}

func loadRuntimeConfig(args []string, protocolVersion, linkBinaryVersion string) (runtimeConfig, error) {
	flags := flag.NewFlagSet("devshardd", flag.ContinueOnError)
	port := flags.Int("port", 9500, "HTTP listen port (set by versiond)")
	dataDir := flags.String("data-dir", "/var/lib/devshardd", "data directory for sqlite state (set by versiond)")
	if err := flags.Parse(args); err != nil {
		return runtimeConfig{}, err
	}

	if protocolVersion == "" {
		return runtimeConfig{}, fmt.Errorf("empty protocol version")
	}

	binaryLogVersion, err := validateBinaryLogVersion(
		os.Getenv("DEVSHARD_BINARY_LOG_VERSION"),
		linkBinaryVersion,
		protocolVersion,
	)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("binary log version: %w", err)
	}

	retryInterval, err := parseDurationEnv("DEVSHARD_VALIDATION_RETRY_INTERVAL", session.DefaultRetryInterval)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("DEVSHARD_VALIDATION_RETRY_INTERVAL: %w", err)
	}

	leaseTTL, err := parseDurationEnv("DEVSHARD_VALIDATION_LEASE_TTL", session.DefaultLeaseTTL)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("DEVSHARD_VALIDATION_LEASE_TTL: %w", err)
	}

	return runtimeConfig{
		Port:                    *port,
		DataDir:                 *dataDir,
		BinaryLogVersion:        binaryLogVersion,
		RuntimeVersion:          protocolVersion,
		ProtocolVersion:         protocolVersion,
		NodeManagerAddr:         envOr("NODE_MANAGER_ADDR", "localhost:9400"),
		ValidationRetryInterval: retryInterval,
		ValidationLeaseTTL:      leaseTTL,
		Node:                    loadNodeConfigFromEnv(),
	}, nil
}

// validateBinaryLogVersion checks DEVSHARD_BINARY_LOG_VERSION against the link-time
// binary stamp. When unset (standalone), the link stamp is used for log prefix.
// When versiond passes the governance slot name (legacy path: binary lacks
// --print-binary-version), env matching protocolVersion is accepted.
func validateBinaryLogVersion(envValue, linkBinaryVersion, protocolVersion string) (string, error) {
	if envValue == "" {
		if linkBinaryVersion == "" {
			return "", fmt.Errorf("empty link binary version")
		}
		return linkBinaryVersion, nil
	}
	if linkBinaryVersion == "" {
		return "", fmt.Errorf("binary log version %q provided but link stamp is empty", envValue)
	}
	if envValue == linkBinaryVersion || envValue == protocolVersion {
		return envValue, nil
	}
	return envValue, fmt.Errorf("binary log version %q does not match link stamp %q", envValue, linkBinaryVersion)
}

// loadNodeConfigFromEnv builds a ChainNodeConfig from the same env vars
// dapi's init-docker.sh already uses (NODE_HOST, KEY_NAME, KEYRING_BACKEND,
// KEYRING_PASSWORD, KEYRING_DIR). Reusing these names avoids inventing
// devshardd-only patterns: anything that exports them for dapi automatically
// configures devshardd too. Defaults match production: file keyring backend,
// /root/.inference dir.
func loadNodeConfigFromEnv() ChainNodeConfig {
	nodeHost := envOr("NODE_HOST", "node")
	chainRPC := strings.TrimSpace(os.Getenv("NODE_RPC_URL"))
	if chainRPC == "" {
		chainRPC = "http://" + nodeHost + ":26657"
	}
	return ChainNodeConfig{
		ChainRpcUrl:         chainRPC,
		ChainGrpcUrl:        envOr("NODE_GRPC_URL", nodeHost+":9090"),
		ChainID:             envOr("CHAIN_ID", ""),
		KeyringBackend:      envOr("KEYRING_BACKEND", "file"),
		KeyringDir:          envOr("KEYRING_DIR", "/root/.inference"),
		SignerKeyName:       envOr("KEY_NAME", ""),
		KeyringPassword:     os.Getenv("KEYRING_PASSWORD"),
		AccountPubKeyBase64: os.Getenv("ACCOUNT_PUBKEY"),
	}
}

// buildApiAccount constructs an ApiAccount for devshardd using the same split
// identity model as dapi:
//   - ACCOUNT_PUBKEY is the cold participant account recorded on chain
//   - KEY_NAME selects the signing key used by the process (warm key on joins)
//
// If ACCOUNT_PUBKEY is unset we fall back to the signer pubkey. That keeps the
// genesis test path working, where signer and account are the same key.
func buildApiAccount(kr keyring.Keyring, nodeConfig ChainNodeConfig) (ApiAccount, error) {
	if nodeConfig.SignerKeyName == "" {
		return ApiAccount{}, fmt.Errorf("KEY_NAME is required")
	}
	record, err := kr.Key(nodeConfig.SignerKeyName)
	if err != nil {
		return ApiAccount{}, fmt.Errorf("get signer %q: %w", nodeConfig.SignerKeyName, err)
	}
	signerPubKey, err := record.GetPubKey()
	if err != nil {
		return ApiAccount{}, fmt.Errorf("signer pubkey: %w", err)
	}

	accountKey := signerPubKey
	if nodeConfig.AccountPubKeyBase64 != "" {
		pubKeyBytes, decodeErr := base64.StdEncoding.DecodeString(nodeConfig.AccountPubKeyBase64)
		if decodeErr != nil {
			return ApiAccount{}, fmt.Errorf("decode ACCOUNT_PUBKEY: %w", decodeErr)
		}
		accountKey = &secp256k1.PubKey{Key: pubKeyBytes}
	}

	return ApiAccount{
		AccountKey:    accountKey,
		SignerRecord:  record,
		AddressPrefix: "gonka",
	}, nil
}

// buildKeyring opens the cosmos keyring for the given config.
// For the file backend, the password is read from nodeConfig.KeyringPassword
// so non-interactive processes can sign without a terminal prompt.
func buildKeyring(nodeConfig ChainNodeConfig) (keyring.Keyring, error) {
	keyringDir, err := expandHome(nodeConfig.KeyringDir)
	if err != nil {
		return nil, err
	}

	reg := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(reg)
	cdc := codec.NewProtoCodec(reg)

	if nodeConfig.KeyringBackend == keyring.BackendFile {
		kr, err := keyring.New("inferenced", nodeConfig.KeyringBackend, keyringDir,
			strings.NewReader(nodeConfig.KeyringPassword), cdc)
		if err != nil {
			return nil, fmt.Errorf("file keyring: %w", err)
		}
		return kr, nil
	}

	kr, err := keyring.New("inferenced", nodeConfig.KeyringBackend, keyringDir, nil, cdc)
	if err != nil {
		return nil, fmt.Errorf("keyring: %w", err)
	}
	return kr, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseDurationEnv parses a duration env var. Returns fallback if the var is unset.
func parseDurationEnv(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", v, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be > 0", v)
	}
	return d, nil
}

func expandHome(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return filepath.Abs(path)
}
