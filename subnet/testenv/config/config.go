package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the complete testenv configuration.
// It is the single source of truth for both docker-compose generation and the
// mock server / subnethost binaries.
type Config struct {
	EscrowID     string        `yaml:"escrow_id"`
	Slots        int           `yaml:"slots"`
	Amount       uint64        `yaml:"amount"`
	TokenPrice   uint64        `yaml:"token_price"`
	AppHash      string        `yaml:"app_hash"` // hex-encoded 32-byte hash
	// CreatorAddress is the escrow owner address returned by the mock server's
	// GetEscrow (on-chain CreatorAddress). If empty, EffectiveCreatorAddress()
	// uses user.address so operator key and escrow stay aligned.
	// Set explicitly to a different value to simulate creator/operator mismatch (403 tests).
	CreatorAddress string        `yaml:"creator_address"`
	Participants   []Participant  `yaml:"participants"`
	Network        NetworkCfg    `yaml:"network"`
	MockServer     MockServerCfg `yaml:"mock_server"`
	// User holds the subnetctl operator key (signs requests to participants).
	// gencompose generates private_key_hex and address if absent.
	User UserCfg `yaml:"user"`
}

// Participant represents one subnet host container.
type Participant struct {
	// PrivateKeyHex is the secp256k1 private key in hex.
	// Left empty in the template; gencompose fills it in.
	PrivateKeyHex string `yaml:"private_key_hex"`
	// Address is the bech32 "gonka…" address derived from PrivateKeyHex.
	// Set by gencompose; used by the mock server to answer GetParticipant.
	Address string `yaml:"address"`
	// SlotIDs lists which escrow slot indices this participant holds.
	// Set by gencompose (round-robin); used by the mock server to build the slots array.
	SlotIDs []int `yaml:"slot_ids"`
	// Name is the docker-compose service name, e.g. "participant-0".
	// Set by gencompose.
	Name string `yaml:"name"`
	// IP is the static container IP, e.g. "172.30.0.10".
	// Set by gencompose.
	IP string `yaml:"ip"`
	// Port is the HTTP listen port for the subnet host transport server.
	Port int `yaml:"port"`
}

// NetworkCfg holds docker network settings.
type NetworkCfg struct {
	// Subnet is the docker network CIDR, e.g. "172.30.0.0/24".
	Subnet string `yaml:"subnet"`
	// BaseIP is the first three octets, e.g. "172.30.0".
	// The mock server gets .2; participants start at .10.
	BaseIP string `yaml:"base_ip"`
}

// MockServerCfg holds settings for the mock mainnet gRPC server.
type MockServerCfg struct {
	Port int `yaml:"port"`
	// IP assigned in docker-compose, e.g. "172.30.0.2".
	IP string `yaml:"ip"`
	// Name is the docker-compose service name.
	Name string `yaml:"name"`
}

// UserCfg holds the subnetctl operator identity (signs HTTP requests to hosts).
type UserCfg struct {
	// PrivateKeyHex is the secp256k1 private key in hex.
	// Left empty in default.yaml; gencompose fills it in.
	PrivateKeyHex string `yaml:"private_key_hex"`
	// Address is the bech32 "gonka…" address derived from PrivateKeyHex.
	// Set by gencompose; must match config.creator_address (or escrow default) for inference to succeed.
	Address string `yaml:"address"`
	// Port is the HTTP listen port for the subnetctl proxy (default 8081).
	Port int `yaml:"port"`
}

// Defaults applied when fields are zero-valued.
const (
	DefaultEscrowID     = "1"
	DefaultSlots        = 16
	DefaultAmount       = 1_000_000
	DefaultTokenPrice   = 1
	DefaultNetworkCIDR  = "172.30.0.0/24"
	DefaultBaseIP       = "172.30.0"
	DefaultMockPort     = 9090
	DefaultMockIP       = "172.30.0.2"
	DefaultMockName     = "mock-server"
	DefaultHostPort     = 8080
	DefaultParticipants = 10
	DefaultSubnetctlPort = 8081
)

// DefaultAppHash is sha256("testenv") used when AppHash is not specified.
var DefaultAppHash = func() string {
	h := sha256.Sum256([]byte("testenv"))
	return hex.EncodeToString(h[:])
}()

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

// Save writes the config to path as YAML.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// Validate checks that the config is internally consistent.
func (c *Config) Validate() error {
	if len(c.Participants) == 0 {
		return errors.New("at least one participant required")
	}
	if c.Slots < len(c.Participants) {
		return fmt.Errorf("slots (%d) must be >= participant count (%d)", c.Slots, len(c.Participants))
	}
	if c.Slots > 128 {
		return errors.New("slots must be <= 128 (MaxGroupSize)")
	}
	return nil
}

// ParticipantURL returns the HTTP URL for a participant container.
func (p *Participant) ParticipantURL() string {
	return fmt.Sprintf("http://%s:%d", p.Name, p.Port)
}

// SlotsArray returns the flat []string slice for the escrow, one entry per slot.
// Index i contains the address of the participant assigned to slot i.
func (c *Config) SlotsArray() []string {
	// Build a slot-index → address lookup from all participants.
	slotToAddr := make(map[int]string, c.Slots)
	for _, p := range c.Participants {
		for _, sid := range p.SlotIDs {
			slotToAddr[sid] = p.Address
		}
	}
	slots := make([]string, c.Slots)
	for i := range slots {
		slots[i] = slotToAddr[i]
	}
	return slots
}

// EffectiveCreatorAddress returns the address the mock server puts in GetEscrow.
// If creator_address is set in YAML, that wins; otherwise user.address (operator).
func (c *Config) EffectiveCreatorAddress() string {
	if s := strings.TrimSpace(c.CreatorAddress); s != "" {
		return s
	}
	return strings.TrimSpace(c.User.Address)
}

// ParticipantByAddress returns the participant for the given address, or nil.
func (c *Config) ParticipantByAddress(addr string) *Participant {
	for i := range c.Participants {
		if c.Participants[i].Address == addr {
			return &c.Participants[i]
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.EscrowID == "" {
		c.EscrowID = DefaultEscrowID
	}
	if c.Slots == 0 {
		c.Slots = DefaultSlots
	}
	if c.Amount == 0 {
		c.Amount = DefaultAmount
	}
	if c.TokenPrice == 0 {
		c.TokenPrice = DefaultTokenPrice
	}
	if c.AppHash == "" {
		c.AppHash = DefaultAppHash
	}
	if c.Network.Subnet == "" {
		c.Network.Subnet = DefaultNetworkCIDR
	}
	if c.Network.BaseIP == "" {
		c.Network.BaseIP = DefaultBaseIP
	}
	if c.MockServer.Port == 0 {
		c.MockServer.Port = DefaultMockPort
	}
	if c.MockServer.IP == "" {
		c.MockServer.IP = DefaultMockIP
	}
	if c.MockServer.Name == "" {
		c.MockServer.Name = DefaultMockName
	}
	for i := range c.Participants {
		if c.Participants[i].Port == 0 {
			c.Participants[i].Port = DefaultHostPort
		}
	}
	if c.User.Port == 0 {
		c.User.Port = DefaultSubnetctlPort
	}
}
