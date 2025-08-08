package apiconfig

import (
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"github.com/productscience/inference/x/inference/types"
)

type ConfigManager struct {
	currentConfig  Config
	KoanProvider   koanf.Provider
	WriterProvider WriteCloserProvider
	mutex          sync.Mutex
}

type WriteCloserProvider interface {
	GetWriter() WriteCloser
}

func LoadDefaultConfigManager() (*ConfigManager, error) {
	manager := ConfigManager{
		KoanProvider:   getFileProvider(),
		WriterProvider: NewFileWriteCloserProvider(getConfigPath()),
		mutex:          sync.Mutex{},
	}
	err := manager.Load()
	if err != nil {
		return nil, err
	}
	return &manager, nil
}

func (cm *ConfigManager) Write() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) Load() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	config, err := readConfig(cm.KoanProvider)
	if err != nil {
		return err
	}
	cm.currentConfig = config
	return nil
}

// Need to make sure we pass back a COPY of the ChainNodeConfig to make sure
// we don't modify the original
func (cm *ConfigManager) GetChainNodeConfig() ChainNodeConfig {
	return cm.currentConfig.ChainNode
}

func (cm *ConfigManager) GetApiConfig() ApiConfig {
	return cm.currentConfig.Api
}

func (cm *ConfigManager) GetNodes() []InferenceNodeConfig {
	nodes := make([]InferenceNodeConfig, len(cm.currentConfig.Nodes))
	copy(nodes, cm.currentConfig.Nodes)
	return nodes
}

func (cm *ConfigManager) getConfig() *Config {
	return &cm.currentConfig
}

func (cm *ConfigManager) SetUpgradePlan(plan UpgradePlan) error {
	cm.currentConfig.UpgradePlan = plan
	logging.Info("Setting upgrade plan", types.Config, "plan", plan)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetUpgradePlan() UpgradePlan {
	return cm.currentConfig.UpgradePlan
}

func (cm *ConfigManager) SetHeight(height int64) error {
	cm.currentConfig.CurrentHeight = height
	newVersion, found := cm.currentConfig.NodeVersions.PopIf(height)
	if found {
		logging.Info("New Node Version!", types.Upgrades, "version", newVersion, "oldVersion", cm.currentConfig.CurrentNodeVersion)
		cm.currentConfig.CurrentNodeVersion = newVersion
	}
	logging.Info("Setting height", types.Config, "height", height)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetCurrentNodeVersion() string {
	return cm.currentConfig.CurrentNodeVersion
}

func (cm *ConfigManager) SetValidationParams(params ValidationParamsCache) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.ValidationParams = params
	logging.Info("Setting validation params", types.Config, "params", params)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetValidationParams() ValidationParamsCache {
	return cm.currentConfig.ValidationParams
}

func (cm *ConfigManager) SetBandwidthParams(params BandwidthParamsCache) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.BandwidthParams = params
	logging.Info("Setting bandwidth params", types.Config, "params", params)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetBandwidthParams() BandwidthParamsCache {
	return cm.currentConfig.BandwidthParams
}

func (cm *ConfigManager) AddNodeVersion(height int64, version string) error {
	if !cm.currentConfig.NodeVersions.Insert(height, version) {
		return nil
	}
	logging.Info("Adding node version", types.Upgrades, "height", height, "version", version)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetHeight() int64 {
	return cm.currentConfig.CurrentHeight
}

func (cm *ConfigManager) SetPreviousSeed(seed SeedInfo) error {
	cm.currentConfig.PreviousSeed = seed
	logging.Info("Setting previous seed", types.Config, "seed", seed)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetPreviousSeed() SeedInfo {
	return cm.currentConfig.PreviousSeed
}

func (cm *ConfigManager) SetCurrentSeed(seed SeedInfo) error {
	cm.currentConfig.CurrentSeed = seed
	logging.Info("Setting current seed", types.Config, "seed", seed)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetCurrentSeed() SeedInfo {
	return cm.currentConfig.CurrentSeed
}

func (cm *ConfigManager) SetUpcomingSeed(seed SeedInfo) error {
	cm.currentConfig.UpcomingSeed = seed
	logging.Info("Setting upcoming seed", types.Config, "seed", seed)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetUpcomingSeed() SeedInfo {
	return cm.currentConfig.UpcomingSeed
}

func (cm *ConfigManager) SetNodes(nodes []InferenceNodeConfig) error {
	cm.currentConfig.Nodes = nodes
	logging.Info("Setting nodes", types.Config, "nodes", nodes)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) CreateWorkerKey() (string, error) {
	workerKey := ed25519.GenPrivKey()
	workerPublicKey := workerKey.PubKey()
	workerPublicKeyString := base64.StdEncoding.EncodeToString(workerPublicKey.Bytes())
	workerPrivateKey := workerKey.Bytes()
	workerPrivateKeyString := base64.StdEncoding.EncodeToString(workerPrivateKey)
	cm.currentConfig.MLNodeKeyConfig.WorkerPrivateKey = workerPrivateKeyString
	cm.currentConfig.MLNodeKeyConfig.WorkerPublicKey = workerPublicKeyString
	err := cm.Write()
	if err != nil {
		return "", err
	}
	return workerPublicKeyString, nil
}

func getFileProvider() koanf.Provider {
	configPath := getConfigPath()
	return file.Provider(configPath)
}

func getConfigPath() string {
	configPath := os.Getenv("API_CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml" // Default value if the environment variable is not set
	}
	return configPath
}

type FileWriteCloserProvider struct {
	path string
}

func NewFileWriteCloserProvider(path string) *FileWriteCloserProvider {
	return &FileWriteCloserProvider{path: path}
}

func (f *FileWriteCloserProvider) GetWriter() WriteCloser {
	file, err := os.OpenFile(f.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		log.Fatalf("error opening file at %s: %v", f.path, err)
	}
	return file
}

func readConfig(provider koanf.Provider) (Config, error) {
	k := koanf.New(".")
	parser := yaml.Parser()

	if err := k.Load(provider, parser); err != nil {
		log.Fatalf("error loading config: %v", err)
	}
	err := k.Load(env.Provider("DAPI_", ".", func(s string) string {
		return strings.Replace(strings.ToLower(
			strings.TrimPrefix(s, "DAPI_")), "__", ".", -1)
	}), nil)

	if err != nil {
		log.Fatalf("error loading env: %v", err)
	}
	var config Config
	err = k.Unmarshal("", &config)
	if err != nil {
		log.Fatalf("error unmarshalling config: %v", err)
	}
	if keyName, found := os.LookupEnv("KEY_NAME"); found {
		config.ChainNode.SignerKeyName = keyName
		log.Printf("Loaded KEY_NAME: %+v", keyName)
	}

	if accountPubKey, found := os.LookupEnv("ACCOUNT_PUBKEY"); found {
		config.ChainNode.AccountPublicKey = accountPubKey
		log.Printf("Loaded ACCOUNT_PUBKEY: %+v", accountPubKey)
	}

	if keyRingBackend, found := os.LookupEnv("KEYRING_BACKEND"); found {
		config.ChainNode.KeyringBackend = keyRingBackend
		log.Printf("Loaded KEYRING_BACKEND: %+v", keyRingBackend)
	}

	if keyringPassword, found := os.LookupEnv("KEYRING_PASSWORD"); found {
		config.ChainNode.KeyringPassword = keyringPassword
		log.Printf("Loaded KEYRING_PASSWORD: %+v", keyringPassword)
	} else {
		log.Printf("Warning: KEYRING_PASSWORD environment variable not set - keyring operations may fail")
	}

	if err := loadNodeConfig(&config); err != nil {
		log.Fatalf("error loading node config: %v", err)
	}
	return config, nil
}

func writeConfig(config Config, writer WriteCloser) error {
	k := koanf.New(".")
	parser := yaml.Parser()
	err := k.Load(structs.Provider(config, "koanf"), nil)
	if err != nil {
		logging.Error("error loading config", types.Config, "error", err)
		return err
	}
	output, err := k.Marshal(parser)
	if err != nil {
		logging.Error("error marshalling config", types.Config, "error", err)
		return err
	}
	_, err = writer.Write(output)
	if err != nil {
		logging.Error("error writing config", types.Config, "error", err)
		return err
	}
	return nil
}

type WriteCloser interface {
	Write([]byte) (int, error)
	Close() error
}

// Called once at startup to load additional nodes from a separate config file
func loadNodeConfig(config *Config) error {
	if config.NodeConfigIsMerged {
		logging.Info("Node config already merged. Skipping", types.Config)
		return nil
	}

	nodeConfigPath, found := os.LookupEnv("NODE_CONFIG_PATH")
	if !found || strings.TrimSpace(nodeConfigPath) == "" {
		logging.Info("NODE_CONFIG_PATH not set. No additional nodes will be added to config", types.Config)
		return nil
	}

	logging.Info("Loading and merging node configuration", types.Config, "path", nodeConfigPath)

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
	config.NodeConfigIsMerged = true

	logging.Info("Successfully loaded and merged node configuration",
		types.Config, "new_nodes", len(newNodes),
		"total_nodes", len(config.Nodes))
	return nil
}

func parseInferenceNodesFromNodeConfigJson(nodeConfigPath string) ([]InferenceNodeConfig, error) {
	file, err := os.Open(nodeConfigPath)
	if err != nil {
		logging.Error("Failed to open node config file", types.Config, "error", err)
		return nil, err
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		logging.Error("Failed to read node config file", types.Config, "error", err)
		return nil, err
	}

	var newNodes []InferenceNodeConfig
	if err := json.Unmarshal(bytes, &newNodes); err != nil {
		logging.Error("Failed to parse node config JSON", types.Config, "error", err)
		return nil, err
	}

	return newNodes, nil
}
