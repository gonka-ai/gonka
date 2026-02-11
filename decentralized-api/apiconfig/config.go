package apiconfig

import (
	"fmt"
	"strings"
)

type Config struct {
	Api                 ApiConfig             `koanf:"api" json:"api"`
	Nodes               []InferenceNodeConfig `koanf:"nodes" json:"nodes"`
	NodeConfigIsMerged  bool                  `koanf:"merged_node_config" json:"merged_node_config"`
	ChainNode           ChainNodeConfig       `koanf:"chain_node" json:"chain_node"`
	UpcomingSeed        SeedInfo              `koanf:"upcoming_seed" json:"upcoming_seed"`
	CurrentSeed         SeedInfo              `koanf:"current_seed" json:"current_seed"`
	PreviousSeed        SeedInfo              `koanf:"previous_seed" json:"previous_seed"`
	CurrentHeight       int64                 `koanf:"current_height" json:"current_height"`
	LastProcessedHeight int64                 `koanf:"last_processed_height" json:"last_processed_height"`
	UpgradePlan         UpgradePlan           `koanf:"upgrade_plan" json:"upgrade_plan"`
	MLNodeKeyConfig     MLNodeKeyConfig       `koanf:"ml_node_key_config" json:"ml_node_key_config"`
	Nats                NatsServerConfig      `koanf:"nats" json:"nats"`
	TxBatching          TxBatchingConfig      `koanf:"tx_batching" json:"tx_batching"`
	CurrentNodeVersion       string                   `koanf:"current_node_version" json:"current_node_version"`
	LastUsedVersion          string                   `koanf:"last_used_version" json:"last_used_version"`
	ValidationParams         ValidationParamsCache    `koanf:"validation_params" json:"validation_params"`
	BandwidthParams          BandwidthParamsCache     `koanf:"bandwidth_params" json:"bandwidth_params"`
	TransferAgentAccessCache TransferAgentAccessCache `koanf:"-" json:"-"` // not persisted, synced from chain
}

type NatsServerConfig struct {
	Host                  string `koanf:"host" json:"host"`
	Port                  int    `koanf:"port" json:"port"`
	MaxMessagesAgeSeconds int64  `koanf:"max_messages_age_seconds"`
}

type TxBatchingConfig struct {
	Disabled                        bool `koanf:"disabled" json:"disabled"`
	FlushSize                       int  `koanf:"flush_size" json:"flush_size"`
	FlushTimeoutSeconds             int  `koanf:"flush_timeout_seconds" json:"flush_timeout_seconds"`
	ValidationV2FlushSize           int  `koanf:"validation_v2_flush_size" json:"validation_v2_flush_size"`
	ValidationV2FlushTimeoutSeconds int  `koanf:"validation_v2_flush_timeout_seconds" json:"validation_v2_flush_timeout_seconds"`
	PocCommitIntervalSeconds        int  `koanf:"poc_commit_interval_seconds" json:"poc_commit_interval_seconds"`
}

type UpgradePlan struct {
	Name        string            `koanf:"name" json:"name"`
	Height      int64             `koanf:"height" json:"height"`
	Binaries    map[string]string `koanf:"binaries" json:"binaries"`
	NodeVersion string            `koanf:"node_version" json:"node_version"`
}

type SeedInfo struct {
	Seed       int64  `koanf:"seed" json:"seed"`
	EpochIndex uint64 `koanf:"epoch_index" json:"epoch_index"`
	Signature  string `koanf:"signature" json:"signature"`
	Claimed    bool   `koanf:"claimed" json:"claimed"`
}

type ApiConfig struct {
	Port                  int    `koanf:"port" json:"port"`
	PoCCallbackUrl        string `koanf:"poc_callback_url" json:"poc_callback_url"`
	MlGrpcCallbackAddress string `koanf:"ml_grpc_callback_address" json:"ml_grpc_callback_address"`
	PublicUrl             string `koanf:"public_url" json:"public_url"`
	PublicServerPort      int    `koanf:"public_server_port" json:"public_server_port"`
	MLServerPort          int    `koanf:"ml_server_port" json:"ml_server_port"`
	AdminServerPort       int    `koanf:"admin_server_port" json:"admin_server_port"`
	MlGrpcServerPort      int    `koanf:"ml_grpc_server_port" json:"ml_grpc_server_port"`
	TestMode              bool   `koanf:"test_mode" json:"test_mode"`
}

type ChainNodeConfig struct {
	Url              string `koanf:"url" json:"url"`
	IsGenesis        bool   `koanf:"is_genesis" json:"is_genesis"`
	SeedApiUrl       string `koanf:"seed_api_url" json:"seed_api_url"`
	AccountPublicKey string `koanf:"account_public_key" json:"account_public_key"`
	SignerKeyName    string `koanf:"signer_key_name" json:"signer_key_name"`
	KeyringBackend   string `koanf:"keyring_backend" json:"keyring_backend"`
	KeyringDir       string `koanf:"keyring_dir" json:"keyring_dir"`
	KeyringPassword  string `json:"-"`
}

type MLNodeKeyConfig struct {
	WorkerPublicKey  string `koanf:"worker_public" json:"worker_public"`
	WorkerPrivateKey string `koanf:"worker_private" json:"worker_private"`
}

// IF YOU CHANGE ANY OF THESE STRUCTURES BE SURE TO CHANGE HardwareNode proto in inference-chain!!!
type InferenceNodeConfig struct {
	Host             string                 `koanf:"host" json:"host"`
	InferenceSegment string                 `koanf:"inference_segment" json:"inference_segment"`
	InferencePort    int                    `koanf:"inference_port" json:"inference_port"`
	PoCSegment       string                 `koanf:"poc_segment" json:"poc_segment"`
	PoCPort          int                    `koanf:"poc_port" json:"poc_port"`
	Models           map[string]ModelConfig `koanf:"models" json:"models"`
	Id               string                 `koanf:"id" json:"id"`
	MaxConcurrent    int                    `koanf:"max_concurrent" json:"max_concurrent"`
	Hardware         []Hardware             `koanf:"hardware" json:"hardware"`
}

// ValidateInferenceNodeBasic validates basic fields of an InferenceNodeConfig without checking for duplicates.
// This is useful when loading from JSON before the broker exists.
// Returns an error describing what is wrong, or nil if valid.
func ValidateInferenceNodeBasic(node InferenceNodeConfig) []string {
	var errors []string

	// Validate required fields
	if strings.TrimSpace(node.Id) == "" {
		errors = append(errors, "node id is required and cannot be empty")
	}

	if strings.TrimSpace(node.Host) == "" {
		errors = append(errors, "host is required and cannot be empty")
	}

	if node.InferencePort <= 0 || node.InferencePort > 65535 {
		errors = append(errors, fmt.Sprintf("inference_port must be between 1 and 65535, got %d", node.InferencePort))
	}

	if node.PoCPort <= 0 || node.PoCPort > 65535 {
		errors = append(errors, fmt.Sprintf("poc_port must be between 1 and 65535, got %d", node.PoCPort))
	}

	if node.MaxConcurrent <= 0 {
		errors = append(errors, fmt.Sprintf("max_concurrent must be greater than 0, got %d", node.MaxConcurrent))
	}

	if len(node.Models) == 0 {
		errors = append(errors, "at least one model must be specified")
	}

	return errors
}

func (n InferenceNodeConfig) DeepCopy() InferenceNodeConfig {
	result := n

	if n.Models != nil {
		result.Models = make(map[string]ModelConfig, len(n.Models))
		for k, v := range n.Models {
			modelCopy := v
			if v.Args != nil {
				modelCopy.Args = make([]string, len(v.Args))
				copy(modelCopy.Args, v.Args)
			}
			result.Models[k] = modelCopy
		}
	}

	if n.Hardware != nil {
		result.Hardware = make([]Hardware, len(n.Hardware))
		copy(result.Hardware, n.Hardware)
	}

	return result
}

type ModelConfig struct {
	Args []string `json:"args"`
}

type Hardware struct {
	Type  string `koanf:"type" json:"type"`
	Count uint32 `koanf:"count" json:"count"`
}

type ValidationParamsCache struct {
	TimestampExpiration int64 `koanf:"timestamp_expiration" json:"timestamp_expiration"`
	TimestampAdvance    int64 `koanf:"timestamp_advance" json:"timestamp_advance"`
	ExpirationBlocks    int64 `koanf:"expiration_blocks" json:"expiration_blocks"`
}

type BandwidthParamsCache struct {
	EstimatedLimitsPerBlockKb uint64  `koanf:"estimated_limits_per_block_kb" json:"estimated_limits_per_block_kb"`
	KbPerInputToken           float64 `koanf:"kb_per_input_token" json:"kb_per_input_token"`
	KbPerOutputToken          float64 `koanf:"kb_per_output_token" json:"kb_per_output_token"`
	MaxInferencesPerBlock     uint64  `koanf:"max_inferences_per_block" json:"max_inferences_per_block"`
}

// TransferAgentAccessCache caches the allowed TA addresses for O(1) lookups.
type TransferAgentAccessCache struct {
	AllowedAddresses map[string]struct{} // O(1) lookup
	IsEnabled        bool                // true if whitelist is non-empty
}
