package apiconfig

import (
	"fmt"
	"log/slog"
	"os"
)

func setEnvVars(config *Config) {
	if keyName, found := os.LookupEnv("KEY_NAME"); found {
		slog.Info("Setting config.ChainNode.AccountName to env var", "AccountName", keyName)
		config.ChainNode.AccountName = keyName
	} else {
		slog.Warn("KEY_NAME not set. Config value will be used", "AccountName", config.ChainNode.AccountName)
	}

	if pocCallbackUrl, found := os.LookupEnv("POC_CALLBACK_URL"); found {
		slog.Info("Setting config.Api.PoCCallbackUrl to env var", "PoCCallbackUrl", pocCallbackUrl)
		config.Api.PoCCallbackUrl = pocCallbackUrl
	} else {
		slog.Warn("POC_CALLBACK_URL not set. Config value will be used", "PoCCallbackUrl", config.Api.PoCCallbackUrl)
	}

	if publicUrl, found := os.LookupEnv("PUBLIC_URL"); found {
		slog.Info("Setting config.Api.PublicUrl to env var", "PublicUrl", publicUrl)
		config.Api.PublicUrl = publicUrl
	} else {
		slog.Warn("PUBLIC_URL not set. Config value will be used", "PublicUrl", config.Api.PublicUrl)
	}

	if nodeHost, found := os.LookupEnv("NODE_HOST"); found {
		value := fmt.Sprintf("http://%s:26657", nodeHost)
		slog.Info("Setting config.ChainNode.Url based on NODE_HOST env var", "Url", value)
		config.ChainNode.Url = value
	} else {
		slog.Warn("NODE_HOST not set. Config value will be used", "Url", config.ChainNode.Url)
	}

	if isGenesis, found := os.LookupEnv("IS_GENESIS"); found {
		slog.Info("Setting config.ChainNode.IsGenesis to env var", "IsGenesis", isGenesis)
		config.ChainNode.IsGenesis = isGenesis == "true"
	} else {
		slog.Warn("IS_GENESIS not set. Config value will be used", "IsGenesis", config.ChainNode.IsGenesis)
	}
}

func loadNodeConfig(config *Config) {
	// TODO:
}
