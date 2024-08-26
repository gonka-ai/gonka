package apiconfig

import (
	"decentralized-api/broker"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"log"
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
