package apiconfig

import (
	"decentralized-api/broker"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"golang.org/x/exp/slog"
	"log"
	"os"
	"sync"
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

func (cm *ConfigManager) GetConfig() *Config {
	return &cm.currentConfig
}

func (cm *ConfigManager) SetUpgradePlan(plan UpgradePlan) error {
	cm.currentConfig.UpgradePlan = plan
	slog.Info("Setting upgrade plan", "plan", plan)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetUpgradePlan() UpgradePlan {
	return cm.currentConfig.UpgradePlan
}

func (cm *ConfigManager) SetHeight(height int64) error {
	cm.currentConfig.CurrentHeight = height
	slog.Info("Setting height", "height", height)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetHeight() int64 {
	return cm.currentConfig.CurrentHeight
}

func (cm *ConfigManager) SetPreviousSeed(seed SeedInfo) error {
	cm.currentConfig.PreviousSeed = seed
	slog.Info("Setting previous seed", "seed", seed)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetPreviousSeed() SeedInfo {
	return cm.currentConfig.PreviousSeed
}

func (cm *ConfigManager) SetCurrentSeed(seed SeedInfo) error {
	cm.currentConfig.CurrentSeed = seed
	slog.Info("Setting current seed", "seed", seed)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetCurrentSeed() SeedInfo {
	return cm.currentConfig.CurrentSeed
}

func (cm *ConfigManager) SetUpcomingSeed(seed SeedInfo) error {
	cm.currentConfig.UpcomingSeed = seed
	slog.Info("Setting upcoming seed", "seed", seed)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
}

func (cm *ConfigManager) GetUpcomingSeed() SeedInfo {
	return cm.currentConfig.UpcomingSeed
}

func (cm *ConfigManager) SetNodes(nodes []broker.InferenceNode) error {
	cm.currentConfig.Nodes = nodes
	slog.Info("Setting nodes", "nodes", nodes)
	return writeConfig(cm.currentConfig, cm.WriterProvider.GetWriter())
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
	var config Config
	err := k.Unmarshal("", &config)
	if err != nil {
		log.Fatalf("error unmarshalling config: %v", err)
	}
	return config, nil
}

func writeConfig(config Config, writer WriteCloser) error {
	k := koanf.New(".")
	parser := yaml.Parser()
	err := k.Load(structs.Provider(config, "koanf"), nil)
	if err != nil {
		slog.Error("error loading config", "error", err)
		return err
	}
	output, err := k.Marshal(parser)
	if err != nil {
		slog.Error("error marshalling config", "error", err)
		return err
	}
	_, err = writer.Write(output)
	if err != nil {
		slog.Error("error writing config", "error", err)
		return err
	}
	return nil
}

type WriteCloser interface {
	Write([]byte) (int, error)
	Close() error
}
