package apiconfig

import (
	"decentralized-api/broker"
	"encoding/json"
	"fmt"
	"io"
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

func loadNodeConfig(config *Config) error {
	nodeConfigPath, found := os.LookupEnv("NODE_CONFIG_PATH")
	if !found {
		slog.Info("NODE_CONFIG_PATH not set. No additional nodes will be added to config")
		return nil
	}

	newNodes, err := parseInferenceNodesFromNodeConfigJson(nodeConfigPath)
	if err != nil {
		return err
	}

	// Check for duplicate IDs across both existing and new nodes
	seenIds := make(map[string]bool)

	// First, add existing nodes to the map
	for _, node := range config.Nodes {
		if seenIds[node.Id] {
			return fmt.Errorf("duplicate node ID found in config: %s", node.Id)
		}
		seenIds[node.Id] = true
	}

	// Check new nodes for duplicates
	for _, node := range newNodes {
		if seenIds[node.Id] {
			return fmt.Errorf("duplicate node ID found in config: %s", node.Id)
		}
		seenIds[node.Id] = true
	}

	// Merge new nodes with existing ones
	config.Nodes = append(config.Nodes, newNodes...)

	slog.Info("Successfully loaded and merged node configuration",
		"new_nodes", len(newNodes),
		"total_nodes", len(config.Nodes))
	return nil
}

func parseInferenceNodesFromNodeConfigJson(nodeConfigPath string) ([]broker.InferenceNode, error) {
	file, err := os.Open(nodeConfigPath)
	if err != nil {
		slog.Error("Failed to open node config file", "error", err)
		return nil, err
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		slog.Error("Failed to read node config file", "error", err)
		return nil, err
	}

	var newNodes []broker.InferenceNode
	if err := json.Unmarshal(bytes, &newNodes); err != nil {
		slog.Error("Failed to parse node config JSON", "error", err)
		return nil, err
	}

	return newNodes, nil
}
