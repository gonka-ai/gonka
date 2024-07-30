package main

import (
	"context"
	"github.com/knadh/koanf/providers/file"
	"log"
	"os"
	"time"
)

func readConfig() Config {
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

func main() {
	config := readConfig()
	recorder, err := NewInferenceCosmosClientWithRetry(context.Background(), "cosmos", 5, 5*time.Second, config)
	if err != nil {
		panic(err)
	}
	//go func() {
	//	StartValidationScheduledTask(*recorder, config)
	//}()
	StartInferenceServerWrapper(*recorder, config)
}
