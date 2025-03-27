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
	Port           int    `koanf:"port"`
	PoCCallbackUrl string `koanf:"poc_callback_url"`
	PublicUrl      string `koanf:"public_url"`
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
	Host          string                 `koanf:"host" json:"host"`
	InferencePort int                    `koanf:"inference_port" json:"inference_port"`
	PoCPort       int                    `koanf:"poc_port" json:"poc_port"`
	Models        map[string]ModelConfig `koanf:"models" json:"models"`
	Id            string                 `koanf:"id" json:"id"`
	MaxConcurrent int                    `koanf:"max_concurrent" json:"max_concurrent"`
	Hardware      []Hardware             `koanf:"hardware" json:"hardware"`
}

type ModelConfig struct {
	Args []string `json:"args"`
}

type Hardware struct {
	Type  string `koanf:"type" json:"type"`
	Count uint32 `koanf:"count" json:"count"`
}
