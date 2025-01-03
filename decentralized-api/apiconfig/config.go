package apiconfig

import (
	"decentralized-api/broker"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"log"
	"log/slog"
	"os"
	"sync"
)

type Config struct {
	Api           ApiConfig              `koanf:"api"`
	Nodes         []broker.InferenceNode `koanf:"nodes"`
	ChainNode     ChainNodeConfig        `koanf:"chain_node"`
	UpcomingSeed  SeedInfo               `koanf:"upcoming_seed"`
	CurrentSeed   SeedInfo               `koanf:"current_seed"`
	PreviousSeed  SeedInfo               `koanf:"previous_seed"`
	CurrentHeight int64                  `koanf:"current_height"`
	UpgradePlan   UpgradePlan            `koanf:"upgrade_plan"`
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
	Port int `koanf:"port"`
}

type ChainNodeConfig struct {
	Url            string `koanf:"url"`
	AccountName    string `koanf:"account_name"`
	KeyringBackend string `koanf:"keyring_backend"`
	KeyringDir     string `koanf:"keyring_dir"`
}

func SetUpgradePlan(plan UpgradePlan) error {
	lastSavedConfig.UpgradePlan = plan
	slog.Info("Setting upgrade plan", "plan", plan)
	return WriteConfig(lastSavedConfig)
}

func GetUpgradePlan() UpgradePlan {
	return lastSavedConfig.UpgradePlan
}

func SetHeight(height int64) error {
	lastSavedConfig.CurrentHeight = height
	return WriteConfig(lastSavedConfig)
}

func GetHeight() int64 {
	return lastSavedConfig.CurrentHeight
}

func SetPreviousSeed(seed SeedInfo) error {
	lastSavedConfig.PreviousSeed = seed
	return WriteConfig(lastSavedConfig)
}

func GetPreviousSeed() SeedInfo {
	return lastSavedConfig.PreviousSeed
}

func SetCurrentSeed(seed SeedInfo) error {
	lastSavedConfig.CurrentSeed = seed
	return WriteConfig(lastSavedConfig)
}

func GetCurrentSeed() SeedInfo {
	return lastSavedConfig.CurrentSeed
}

func SetUpcomingSeed(seed SeedInfo) error {
	lastSavedConfig.UpcomingSeed = seed
	return WriteConfig(lastSavedConfig)
}

func GetUpcomingSeed() SeedInfo {
	return lastSavedConfig.UpcomingSeed
}

var lastSavedConfig Config
var configFileMutex = &sync.Mutex{}

func ReadConfig() Config {
	configFileMutex.Lock()
	defer configFileMutex.Unlock()
	k := koanf.New(".")
	parser := yaml.Parser()

	configPath := os.Getenv("API_CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml" // Default value if the environment variable is not set
	}

	// Load the configuration
	if err := k.Load(file.Provider(configPath), parser); err != nil {
		log.Fatalf("error loading config: %v", err)
	}
	var config Config
	err := k.Unmarshal("", &config)
	if err != nil {
		log.Fatalf("error unmarshalling config: %v", err)
	}
	lastSavedConfig = config
	return config
}

func WriteConfig(config Config) error {
	configFileMutex.Lock()
	defer configFileMutex.Unlock()
	k := koanf.New(".")
	parser := yaml.Parser()

	configPath := os.Getenv("API_CONFIG_PATH")
	if configPath == "" {
		log.Printf("API_CONFIG_PATH not set, using default config.yaml")
		configPath = "config.yaml" // Default value if the environment variable is not set
	} else {
		log.Printf("API_CONFIG_PATH set to %s", configPath)
	}

	err := k.Set("nodes", config.Nodes)
	if err != nil {
		slog.Error("error setting config", "error", err)
		return err
	}
	err = k.Set("api", config.Api)
	if err != nil {
		slog.Error("error setting config", "error", err)
		return err
	}
	err = k.Set("chain_node", config.ChainNode)
	if err != nil {
		slog.Error("error setting config", "error", err)
		return err
	}
	err = k.Set("upcoming_seed", config.UpcomingSeed)
	if err != nil {
		slog.Error("error setting config", "error", err)
		return err
	}
	err = k.Set("current_seed", config.CurrentSeed)
	if err != nil {
		slog.Error("error setting config", "error", err)
		return err
	}
	err = k.Set("previous_seed", config.PreviousSeed)
	if err != nil {
		slog.Error("error setting config", "error", err)
		return err
	}
	err = k.Set("current_height", config.CurrentHeight)
	if err != nil {
		slog.Error("error setting config", "error", err)
		return err
	}
	err = k.Set("upgrade_plan", config.UpgradePlan)
	if err != nil {
		slog.Error("error setting upgrade_plan", "error", err)
		return err
	}
	output, err := k.Marshal(parser)
	if err != nil {
		slog.Error("error marshalling config", "error", err)
		return err
	}
	err = os.WriteFile(configPath, output, 0755)
	if err != nil {
		slog.Error("error writing config", "error", err)
		return err
	}
	lastSavedConfig = config
	return nil
}
