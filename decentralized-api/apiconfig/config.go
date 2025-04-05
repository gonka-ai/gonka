package apiconfig

type Config struct {
	Api                ApiConfig             `koanf:"api"`
	Nodes              []InferenceNodeConfig `koanf:"nodes"`
	NodeConfigIsMerged bool                  `koanf:"merged_node_config"`
	ChainNode          ChainNodeConfig       `koanf:"chain_node"`
	UpcomingSeed       SeedInfo              `koanf:"upcoming_seed"`
	CurrentSeed        SeedInfo              `koanf:"current_seed"`
	PreviousSeed       SeedInfo              `koanf:"previous_seed"`
	CurrentHeight      int64                 `koanf:"current_height"`
	UpgradePlan        UpgradePlan           `koanf:"upgrade_plan"`
	KeyConfig          KeyConfig             `koanf:"key_config"`
	NodeVersions       NodeVersionStack      `koanf:"node_versions"`
	CurrentNodeVersion string                `koanf:"current_node_version"`
}

type NodeVersionStack struct {
	Versions []NodeVersion `koanf:"versions"`
}

func (nvs *NodeVersionStack) peek() *NodeVersion {
	if len(nvs.Versions) == 0 {
		return nil
	}
	return &nvs.Versions[len(nvs.Versions)-1]
}

func (nvs *NodeVersionStack) pop() *NodeVersion {
	nv := nvs.peek()
	nvs.Versions = nvs.Versions[:len(nvs.Versions)-1]
	return nv
}

func (nvs *NodeVersionStack) PopIf(height int64) (string, bool) {
	if len(nvs.Versions) == 0 {
		return "", false
	}
	peek := nvs.peek()
	var result *NodeVersion = &NodeVersion{}
	for peek != nil && height >= peek.Height {
		result = nvs.pop()
		peek = nvs.peek()
	}
	return result.Version, result.Version != ""
}

func (nvs *NodeVersionStack) Insert(height int64, version string) bool {
	newVersion := NodeVersion{Height: height, Version: version}
	versionsWithInserts := make([]NodeVersion, 0, len(nvs.Versions)+1)
	inserted := false

	for _, v := range nvs.Versions {
		if !inserted && v.Height < height {
			versionsWithInserts = append(versionsWithInserts, newVersion)
			inserted = true
		}
		if newVersion.Version == v.Version && newVersion.Height == v.Height {
			return false
		}
		versionsWithInserts = append(versionsWithInserts, v)
	}

	if !inserted {
		versionsWithInserts = append(versionsWithInserts, newVersion)
	}

	nvs.Versions = versionsWithInserts
	return true
}

type NodeVersion struct {
	Height  int64  `koanf:"height"`
	Version string `koanf:"version"`
}

type UpgradePlan struct {
	Name     string            `koanf:"name"`
	Height   int64             `koanf:"height"`
	Binaries map[string]string `koanf:"binaries"`
}

type SeedInfo struct {
	Seed      int64  `koanf:"seed"`
	Height    int64  `koanf:"height"`
	Signature string `koanf:"signature"`
}

type ApiConfig struct {
	Port             int    `koanf:"port"`
	PoCCallbackUrl   string `koanf:"poc_callback_url"`
	PublicUrl        string `koanf:"public_url"`
	PublicServerPort int    `koanf:"public_server_port"`
	MLServerPort     int    `koanf:"ml_server_port"`
}

type ChainNodeConfig struct {
	Url            string `koanf:"url"`
	AccountName    string `koanf:"account_name"`
	KeyringBackend string `koanf:"keyring_backend"`
	KeyringDir     string `koanf:"keyring_dir"`
	IsGenesis      bool   `koanf:"is_genesis"`
	SeedApiUrl     string `koanf:"seed_api_url"`
}

type KeyConfig struct {
	WorkerPublicKey  string `koanf:"worker_public"`
	WorkerPrivateKey string `koanf:"worker_private"`
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
	Version          string                 `koanf:"version" json:"version"`
}

type ModelConfig struct {
	Args []string `json:"args"`
}

type Hardware struct {
	Type  string `koanf:"type" json:"type"`
	Count uint32 `koanf:"count" json:"count"`
}
