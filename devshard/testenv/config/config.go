// Package config loads and validates the top-level testenv config.yaml.
//
// The schema is the single source of truth for:
//
//   - cmd/mockchain — seeds the escrow and participant registry
//   - cmd/heightsyncd (Phase 3) — pins the validator pubkey
//   - cmd/devshardd-testenv (Phase 8) — picks its signer and slot ids
//   - cmd/gencompose (Phase 10) — renders docker-compose.yml
//
// Fields that are only needed by later phases have godoc comments but no
// validation yet; gencompose will fill them in before the compose stack
// starts.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"devshard/blockoracle"
	"devshard/signing"
)

// Config is the top-level testenv descriptor.
type Config struct {
	Chain      ChainCfg      `yaml:"chain"`
	MockChain  MockChainCfg  `yaml:"mock_chain"`
	HeightSync HeightSyncCfg `yaml:"height_sync"`
	Devshard   DevshardCfg   `yaml:"devshard"`
	Escrow     EscrowCfg     `yaml:"escrow"`
	Hosts      []HostCfg     `yaml:"hosts"`
	User       UserCfg       `yaml:"user"`
	Engine     EngineCfg     `yaml:"engine"`
	Network    NetworkCfg    `yaml:"network"`
}

// ChainCfg pins the chain identity consumed by the block oracle and the
// settlement protocol. Must match height_sync's chain id.
type ChainCfg struct {
	ID        string `yaml:"id"`
	BlockTime string `yaml:"block_time"`
}

// MockChainCfg is the listen address of cmd/mockchain.
type MockChainCfg struct {
	Port int    `yaml:"port"`
	Host string `yaml:"host"`
}

// HeightSyncCfg describes both sides of the height-sync contract:
//
//   - URL is the consumer-side endpoint: every devshardd / mockdapi
//     subscribes to URL. Hosts trust the oracle (no signature
//     verification on ingest) but still cache the full header including
//     Commit.Signatures so they can forward proofs downstream.
//   - Validators is the producer-side validator set. Each fabricated
//     header is multi-signed by (most of) these validators; the
//     remaining power is always strictly > 3/4 of total so external
//     auditors' 2/3 checks always pass.
//   - Port + InitialHeight + Seed + BlockInterval are server-side; only
//     the heightsyncd binary reads them.
//
// gencompose (Phase 10) is responsible for populating Validators with
// real hex keys so producer and non-host consumers (devshardctl) agree
// on the pinned set without operator intervention.
type HeightSyncCfg struct {
	URL           string                   `yaml:"url"`
	Validators    []HeightSyncValidatorCfg `yaml:"validators"`
	Port          int                      `yaml:"port"`
	InitialHeight int64                    `yaml:"initial_height"`
	Seed          int64                    `yaml:"seed"`
	// BlockInterval overrides chain.block_time for the producer only.
	// Leave empty to inherit chain.block_time.
	BlockInterval string `yaml:"block_interval"`
	// BlockIntervalDelta adds symmetric jitter around the mean block interval.
	// Example: block_interval=1s, block_interval_delta=250ms => sampled interval
	// in [750ms, 1250ms]. Leave empty or invalid to disable jitter.
	BlockIntervalDelta string `yaml:"block_interval_delta"`
	// AnchorPeriodNonces is K in envelope nonces between sync turns (see
	// HEIGHT_SYNC_PROTOCOL_PROPOSAL). Zero or unset defaults to 10 after
	// ApplyDefaults.
	AnchorPeriodNonces int `yaml:"anchor_period_nonces"`
	// SyncTurnSlots is the width of each sync-turn window in envelope
	// nonces. Zero or unset defaults to devshard.group_size after
	// ApplyDefaults. Must satisfy AnchorPeriodNonces >= SyncTurnSlots.
	SyncTurnSlots int `yaml:"sync_turn_slots"`
}

// HeightSyncValidatorCfg is one pinned mock-mainnet validator.
//
// PrivateKeyHex is the hex-encoded secp256k1 private key; gencompose
// fills real values before docker-compose runs, so TODO placeholders in
// the committed config are expected. Power defaults to 1 when unset so
// the common uniform-weight case stays ergonomic.
type HeightSyncValidatorCfg struct {
	PrivateKeyHex string `yaml:"private_key_hex"`
	Power         int64  `yaml:"power"`
}

// DevshardCfg pins the devshard group shape. GroupSize defaults to
// len(Hosts) when unset.
type DevshardCfg struct {
	GroupSize int `yaml:"group_size"`
}

// EscrowCfg is the seeded escrow.
type EscrowCfg struct {
	ID             string `yaml:"id"`
	Version        string `yaml:"version"`
	Amount         uint64 `yaml:"amount"`
	TokenPrice     uint64 `yaml:"token_price"`
	AppHash        string `yaml:"app_hash"` // hex-encoded 32-byte hash; empty → default
	CreatorAddress string `yaml:"creator_address"`
	Slots          int    `yaml:"slots"`
}

// HostCfg is one devshardd-testenv participant.
type HostCfg struct {
	ID            string `yaml:"id"`
	PrivateKeyHex string `yaml:"private_key_hex"`
	Address       string `yaml:"address"`  // derived by gencompose
	SlotIDs       []int  `yaml:"slot_ids"` // set by gencompose
	URL           string `yaml:"url"`      // http://<service>:<port>; set by gencompose
	Port          int    `yaml:"port"`
	IP            string `yaml:"ip"`
	// PublicMetricsPort is the host-loopback TCP port gencompose maps to this
	// container's METRICS_PORT (Prometheus /metrics). Citest §7.2 I2a uses it
	// to read devshardd_height_at_latest_nonce directly from each process.
	PublicMetricsPort int `yaml:"public_metrics_port"`
}

// UserCfg is the devshardctl operator identity.
type UserCfg struct {
	PrivateKeyHex string `yaml:"private_key_hex"`
	Address       string `yaml:"address"`
	Port          int    `yaml:"port"`
}

// EngineCfg is the stub inference/validation engine dial (Phase 7).
type EngineCfg struct {
	Inference  EngineModeCfg `yaml:"inference"`
	Validation EngineModeCfg `yaml:"validation"`
}

// EngineModeCfg wraps a single engine's knobs.
type EngineModeCfg struct {
	Mode string `yaml:"mode"`
	Seed int64  `yaml:"seed"`
}

// NetworkCfg holds docker-compose network knobs.
type NetworkCfg struct {
	Subnet string `yaml:"subnet"`
	BaseIP string `yaml:"base_ip"`
}

// Defaults exported for tests and gencompose.
const (
	DefaultChainID                      = "gonka-testenv-1"
	DefaultBlockTime                    = "6s"
	DefaultMockChainPort                = 9090
	DefaultMockChainHost                = "mock-chain"
	DefaultHeightSyncPort               = 9100
	DefaultHeightSyncHost               = "height-sync"
	DefaultHeightSyncValidators         = 10
	DefaultHeightSyncBlockIntervalDelta = "3s"
	DefaultValidatorPower               = int64(1)
	DefaultEscrowID                     = "1"
	DefaultEscrowVersion                = "v1"
	DefaultEscrowAmount                 = uint64(1_000_000)
	DefaultTokenPrice                   = uint64(1)
	DefaultSlots                        = 16
	DefaultUserPort                     = 8081
	DefaultHostPort                     = 8080
	// DefaultHostPublicMetricsPort is host 0's published /metrics port
	// (127.0.0.1:<Default+i>:9600). See gencompose devshardd ports block.
	DefaultHostPublicMetricsPort = 19600
	DefaultNetworkCIDR           = "172.30.0.0/24"
	DefaultNetworkBaseIP         = "172.30.0"
	DefaultEngineMode            = "deterministic"
	// DefaultAnchorPeriodNonces is K for height-sync Anchor cadence (envelope nonces).
	// Must be ≥ default sync_turn_slots (devshard.group_size, default len(hosts)=10
	// after gencompose bootstrap). Citest uses a smaller profile via
	// scripts/gen-integration-testenv-config.sh (K=8, four hosts).
	DefaultAnchorPeriodNonces = 16
)

// DefaultAppHash is sha256("devshard-testenv"); used when escrow.app_hash
// is empty.
var DefaultAppHash = func() string {
	h := sha256.Sum256([]byte("devshard-testenv"))
	return hex.EncodeToString(h[:])
}()

// Save marshals the Config back to YAML at path. Comments in the input
// file are not preserved — gencompose owns the file and stamps a
// "generated" banner at the top so operators don't accidentally edit
// by hand.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	const banner = "# Auto-generated by gencompose — edit the input flags / seed config\n" +
		"# and re-run `go run ./testenv/cmd/gencompose` instead of editing.\n"
	return os.WriteFile(path, append([]byte(banner), data...), 0o644)
}

// Load reads a YAML config file and applies defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// Validate enforces the invariants cmd/mockchain relies on at startup.
//
// Phase 2 is deliberately permissive about fields that gencompose fills
// in later (host addresses, IPs, ports). The invariants we do enforce:
//
//   - At least one host is declared.
//   - escrow.slots ≥ len(hosts) and ≤ 128.
//   - chain.id is non-empty.
func (c *Config) Validate() error {
	if c.Chain.ID == "" {
		return errors.New("chain.id must not be empty")
	}
	if len(c.Hosts) == 0 {
		return errors.New("at least one host is required")
	}
	if c.Escrow.Slots < len(c.Hosts) {
		return fmt.Errorf("escrow.slots (%d) must be ≥ number of hosts (%d)",
			c.Escrow.Slots, len(c.Hosts))
	}
	if c.Escrow.Slots > 128 {
		return fmt.Errorf("escrow.slots (%d) exceeds 128", c.Escrow.Slots)
	}
	if c.HeightSync.AnchorPeriodNonces < 0 {
		return errors.New("height_sync.anchor_period_nonces must be >= 0")
	}
	if c.HeightSync.SyncTurnSlots < 0 {
		return errors.New("height_sync.sync_turn_slots must be >= 0")
	}
	if c.HeightSync.SyncTurnSlots > 0 && c.HeightSync.AnchorPeriodNonces > 0 &&
		c.HeightSync.AnchorPeriodNonces < c.HeightSync.SyncTurnSlots {
		return fmt.Errorf("height_sync.anchor_period_nonces (%d) must be >= sync_turn_slots (%d)",
			c.HeightSync.AnchorPeriodNonces, c.HeightSync.SyncTurnSlots)
	}
	seenPub := make(map[int]string)
	for _, h := range c.Hosts {
		if h.PublicMetricsPort <= 0 {
			continue
		}
		if other, ok := seenPub[h.PublicMetricsPort]; ok {
			return fmt.Errorf("hosts %q and %q both use public_metrics_port %d", other, h.ID, h.PublicMetricsPort)
		}
		seenPub[h.PublicMetricsPort] = h.ID
	}
	return nil
}

// EffectiveCreatorAddress returns the address the mock-chain puts in
// GetDevshardEscrowResponse.creator_address. Falls back to user.address
// so subnetctl-style operator identity stays aligned by default.
func (c *Config) EffectiveCreatorAddress() string {
	if s := strings.TrimSpace(c.Escrow.CreatorAddress); s != "" {
		return s
	}
	return strings.TrimSpace(c.User.Address)
}

// SlotsArray returns the []string slot→address mapping, one entry per
// slot. Unassigned slots are empty strings. Matches the old
// subnet-testenv behavior (slot index == array index).
func (c *Config) SlotsArray() []string {
	slots := make([]string, c.Escrow.Slots)
	for _, h := range c.Hosts {
		for _, sid := range h.SlotIDs {
			if sid >= 0 && sid < c.Escrow.Slots {
				slots[sid] = h.Address
			}
		}
	}
	return slots
}

// HostByAddress returns the host descriptor for addr, or nil.
func (c *Config) HostByAddress(addr string) *HostCfg {
	for i := range c.Hosts {
		if c.Hosts[i].Address == addr {
			return &c.Hosts[i]
		}
	}
	return nil
}

// HostURL returns the full URL for a host. Prefers an explicit URL field;
// falls back to http://<id>:<port> otherwise.
func (h *HostCfg) HostURL() string {
	if h.URL != "" {
		return h.URL
	}
	if h.Port == 0 {
		return ""
	}
	svc := h.ID
	if svc == "" {
		svc = "devshardd-testenv"
	}
	return fmt.Sprintf("http://%s:%d", svc, h.Port)
}

// ApplyDefaults stamps every default-eligible field. Load calls this
// automatically; gencompose calls it directly when starting from a
// zero-valued Config.
func (c *Config) ApplyDefaults() { c.applyDefaults() }

func (c *Config) applyDefaults() {
	if c.Chain.ID == "" {
		c.Chain.ID = DefaultChainID
	}
	if c.Chain.BlockTime == "" {
		c.Chain.BlockTime = DefaultBlockTime
	}
	if c.MockChain.Port == 0 {
		c.MockChain.Port = DefaultMockChainPort
	}
	if c.MockChain.Host == "" {
		c.MockChain.Host = DefaultMockChainHost
	}
	if c.HeightSync.Port == 0 {
		c.HeightSync.Port = DefaultHeightSyncPort
	}
	if c.HeightSync.URL == "" {
		c.HeightSync.URL = fmt.Sprintf("http://%s:%d", DefaultHeightSyncHost, c.HeightSync.Port)
	}
	if c.HeightSync.InitialHeight == 0 {
		c.HeightSync.InitialHeight = 1
	}
	if c.HeightSync.BlockIntervalDelta == "" {
		c.HeightSync.BlockIntervalDelta = DefaultHeightSyncBlockIntervalDelta
	}
	if c.Devshard.GroupSize == 0 {
		c.Devshard.GroupSize = len(c.Hosts)
	}
	if c.HeightSync.SyncTurnSlots == 0 {
		c.HeightSync.SyncTurnSlots = c.Devshard.GroupSize
	}
	if c.HeightSync.SyncTurnSlots < 1 {
		c.HeightSync.SyncTurnSlots = 1
	}
	if c.HeightSync.AnchorPeriodNonces == 0 {
		c.HeightSync.AnchorPeriodNonces = DefaultAnchorPeriodNonces
	}
	for i := range c.HeightSync.Validators {
		if c.HeightSync.Validators[i].Power == 0 {
			c.HeightSync.Validators[i].Power = DefaultValidatorPower
		}
	}
	if c.Escrow.ID == "" {
		c.Escrow.ID = DefaultEscrowID
	}
	if c.Escrow.Version == "" {
		c.Escrow.Version = DefaultEscrowVersion
	}
	if c.Escrow.Amount == 0 {
		c.Escrow.Amount = DefaultEscrowAmount
	}
	if c.Escrow.TokenPrice == 0 {
		c.Escrow.TokenPrice = DefaultTokenPrice
	}
	if c.Escrow.AppHash == "" {
		c.Escrow.AppHash = DefaultAppHash
	}
	if c.Escrow.Slots == 0 {
		c.Escrow.Slots = DefaultSlots
	}
	for i := range c.Hosts {
		if c.Hosts[i].Port == 0 {
			c.Hosts[i].Port = DefaultHostPort
		}
		if c.Hosts[i].PublicMetricsPort == 0 {
			c.Hosts[i].PublicMetricsPort = DefaultHostPublicMetricsPort + i
		}
	}
	if c.User.Port == 0 {
		c.User.Port = DefaultUserPort
	}
	if c.Engine.Inference.Mode == "" {
		c.Engine.Inference.Mode = DefaultEngineMode
	}
	if c.Engine.Validation.Mode == "" {
		c.Engine.Validation.Mode = DefaultEngineMode
	}
	if c.Network.Subnet == "" {
		c.Network.Subnet = DefaultNetworkCIDR
	}
	if c.Network.BaseIP == "" {
		c.Network.BaseIP = DefaultNetworkBaseIP
	}
}

// HeightSyncBlockInterval returns the parsed block cadence for the
// height-sync producer. HeightSync.BlockInterval wins over
// Chain.BlockTime when set; both fall back to 1s if unset or unparseable.
func (c *Config) HeightSyncBlockInterval() time.Duration {
	const fallback = time.Second
	for _, v := range []string{c.HeightSync.BlockInterval, c.Chain.BlockTime} {
		if v == "" {
			continue
		}
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

// HeightSyncBlockIntervalDelta returns the parsed symmetric jitter around the
// mean block interval for height-sync. Invalid or empty values disable jitter.
func (c *Config) HeightSyncBlockIntervalDelta() time.Duration {
	if c.HeightSync.BlockIntervalDelta == "" {
		return 0
	}
	d, err := time.ParseDuration(c.HeightSync.BlockIntervalDelta)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// MockdapiStaleAfter returns MOCKDAPI_STALE_AFTER for compose and devshardctl.
// It must exceed the maximum quiet period between height-sync blocks
// (block interval + jitter) so AnchorScheduler does not treat the feed as
// stale between blocks during sync-turn windows.
func (c *Config) MockdapiStaleAfter() time.Duration {
	block := c.HeightSyncBlockInterval()
	delta := c.HeightSyncBlockIntervalDelta()
	d := block + delta + time.Second
	const floor = 10 * time.Second
	if d < floor {
		return floor
	}
	return d
}

// MockdapiStaleAfterString is the compose env value (e.g. "10s").
func (c *Config) MockdapiStaleAfterString() string {
	return c.MockdapiStaleAfter().String()
}

// HeightSyncValidator is the resolved form of a HeightSyncValidatorCfg:
// the hex key has been parsed into a signer and its 20-byte address is
// pre-computed. heightsyncd hands these straight to standalone.Config.
type HeightSyncValidator struct {
	Signer  *signing.Secp256k1Signer
	Address []byte
	Power   int64
}

// HeightSyncValidators parses every entry in c.HeightSync.Validators
// into a usable signer + address + power triple. Returns an error if
// any entry has an empty or malformed private key, or if two entries
// derive to the same address (a common gencompose mistake).
func (c *Config) HeightSyncValidators() ([]HeightSyncValidator, error) {
	if len(c.HeightSync.Validators) == 0 {
		return nil, errors.New("height_sync.validators must declare at least one validator")
	}
	out := make([]HeightSyncValidator, 0, len(c.HeightSync.Validators))
	seen := make(map[string]int, len(c.HeightSync.Validators))
	for i, v := range c.HeightSync.Validators {
		key := strings.TrimSpace(v.PrivateKeyHex)
		if key == "" {
			return nil, fmt.Errorf("height_sync.validators[%d].private_key_hex is empty", i)
		}
		if strings.HasPrefix(key, "TODO") {
			return nil, fmt.Errorf("height_sync.validators[%d].private_key_hex is a TODO placeholder; "+
				"run `make gen-compose` to seed real keys", i)
		}
		signer, err := signing.SignerFromHex(key)
		if err != nil {
			return nil, fmt.Errorf("height_sync.validators[%d].private_key_hex: %w", i, err)
		}
		addr, err := blockoracle.AddressBytes(signer.PublicKeyBytes())
		if err != nil {
			return nil, fmt.Errorf("height_sync.validators[%d] derive address: %w", i, err)
		}
		fp := hex.EncodeToString(addr)
		if prev, dup := seen[fp]; dup {
			return nil, fmt.Errorf(
				"height_sync.validators[%d] duplicates address of validators[%d] (%s)",
				i, prev, fp)
		}
		seen[fp] = i
		power := v.Power
		if power <= 0 {
			power = DefaultValidatorPower
		}
		out = append(out, HeightSyncValidator{
			Signer:  signer,
			Address: addr,
			Power:   power,
		})
	}
	return out, nil
}

// MustAppHash decodes c.Escrow.AppHash into a byte slice. It panics only
// if the hex is malformed, which Validate should already have rejected;
// the helper is provided so callers don't duplicate decode + fallback
// logic at every use site.
func (c *Config) MustAppHash() []byte {
	b, err := hex.DecodeString(c.Escrow.AppHash)
	if err != nil {
		panic(fmt.Sprintf("config: invalid escrow.app_hash %q: %v", c.Escrow.AppHash, err))
	}
	return b
}
