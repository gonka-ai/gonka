package apiconfig

import (
	"decentralized-api/broker"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"log"
	"log/slog"
	"os"
)

type Config struct {
	Api       ApiConfig              `koanf:"api"`
	Nodes     []broker.InferenceNode `koanf:"nodes"`
	ChainNode ChainNodeConfig        `koanf:"chain_node"`
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

func ReadConfig() Config {
	k := koanf.New(".")
	parser := yaml.Parser()

	configPath := os.Getenv("API_CONFIG_PATH")
	if configPath == "" {
		log.Printf("API_CONFIG_PATH not set, using default config.yaml")
		configPath = "config.yaml" // Default value if the environment variable is not set
	} else {
		log.Printf("API_CONFIG_PATH set to %s", configPath)
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
	return config
}

func WriteConfig(config Config) error {
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

	return nil
}
