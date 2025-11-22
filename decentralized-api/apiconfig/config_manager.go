package apiconfig

import (
	"context"
	"database/sql"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

type ConfigManager struct {
	currentConfig  Config
	KoanProvider   koanf.Provider
	WriterProvider WriteCloserProvider
	sqlDb          SqlDatabase
	mutex          sync.Mutex
	configDumpPath string
	sqlitePath     string
}

type WriteCloserProvider interface {
	GetWriter() WriteCloser
}

func LoadDefaultConfigManager() (*ConfigManager, error) {
	return LoadConfigManagerWithPaths(getConfigPath(), getSqlitePath(), os.Getenv("NODE_CONFIG_PATH"))
}

// LoadConfigManagerWithPaths allows tests to supply explicit paths.
func LoadConfigManagerWithPaths(configPath, sqlitePath, nodeConfigPath string) (*ConfigManager, error) {
	defaultDbCfg := SqliteConfig{
		Path: sqlitePath,
	}

	db := NewSQLiteDb(defaultDbCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.BootstrapLocal(ctx); err != nil {
		log.Printf("Error bootstrapping local SQLite DB: %+v", err)
		return nil, err
	}

	manager := ConfigManager{
		KoanProvider:   file.Provider(configPath),
		WriterProvider: NewFileWriteCloserProvider(configPath),
		sqlDb:          db,
		mutex:          sync.Mutex{},
		configDumpPath: filepath.Join(filepath.Dir(sqlitePath), "config-dump.json"),
		sqlitePath:     sqlitePath,
	}
	err := manager.Load()
	if err != nil {
		return nil, err
	}

	err = manager.migrateDynamicDataToDb(ctx)
	if err != nil {
		log.Printf("Error migrating dynamic data to DB: %+v", err)
		return nil, err
	}

	if err = manager.Write(); err != nil {
		log.Printf("Error writing config: %+v", err)
		return nil, err
	}
	log.Printf("Saved static config after load")

	// Hydrate in-memory dynamic state from DB once
	if err := manager.HydrateFromDB(context.Background()); err != nil {
		log.Printf("Error hydrating dynamic data from DB: %+v", err)
		return nil, err
	}
	// Load node config JSON into in-memory struct if it's the very first run
	if err := manager.LoadNodeConfig(ctx, nodeConfigPath); err != nil {
		log.Fatalf("error loading node config: %v", err)
	}

	// Log the resulting config in pretty JSON format for easier debugging
	// Make a copy and sanitize sensitive fields before logging
	sanitized := manager.currentConfig
	sanitized.CurrentSeed.Seed = 0
	sanitized.PreviousSeed.Seed = 0
	sanitized.UpcomingSeed.Seed = 0
	sanitized.MLNodeKeyConfig.WorkerPrivateKey = ""
	if cfgBytes, err := json.MarshalIndent(sanitized, "", "  "); err != nil {
		log.Printf("Error marshaling final config to JSON: %+v", err)
	} else {
		log.Printf("Final loaded config (JSON):\n%s", string(cfgBytes))
	}
	return &manager, nil
}

func (cm *ConfigManager) Write() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	// Write only static fields to config file
	staticCopy := cm.getStaticConfigCopyUnsafe()
	return writeConfig(staticCopy, cm.WriterProvider)
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

func (cm *ConfigManager) GetNatsConfig() NatsServerConfig {
	return cm.currentConfig.Nats
}

func (cm *ConfigManager) GetNodes() []InferenceNodeConfig {
	nodes := make([]InferenceNodeConfig, len(cm.currentConfig.Nodes))
	copy(nodes, cm.currentConfig.Nodes)
	return nodes
}

// SqlDb returns the configured SQL database handle if available
func (cm *ConfigManager) SqlDb() SqlDatabase {
	return cm.sqlDb
}

func (cm *ConfigManager) getConfig() *Config {
	return &cm.currentConfig
}

// GetConfig returns a snapshot copy of the current configuration.
// The returned value should be treated as read-only by callers.
func (cm *ConfigManager) GetConfig() Config {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	return cm.currentConfig
}

func (cm *ConfigManager) GetUpgradePlan() UpgradePlan { return cm.currentConfig.UpgradePlan }

func (cm *ConfigManager) SetUpgradePlan(plan UpgradePlan) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.UpgradePlan = plan
	logging.Info("Setting upgrade plan", types.Config, "plan", plan)
	return nil
}

func (cm *ConfigManager) ClearUpgradePlan() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.UpgradePlan = UpgradePlan{}
	logging.Info("Clearing upgrade plan", types.Config)
	return nil
}

func (cm *ConfigManager) SetHeight(height int64) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.CurrentHeight = height
	logging.Info("Setting height", types.Config, "height", height)
	return nil
}

func (cm *ConfigManager) GetLastProcessedHeight() int64 {
	return cm.currentConfig.LastProcessedHeight
}

func (cm *ConfigManager) SetLastProcessedHeight(height int64) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.LastProcessedHeight = height
	logging.Info("Setting last processed height", types.Config, "height", height)
	return nil
}

func (cm *ConfigManager) GetCurrentNodeVersion() string {
	return cm.currentConfig.CurrentNodeVersion
}

func (cm *ConfigManager) SetCurrentNodeVersion(version string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	oldVersion := cm.currentConfig.CurrentNodeVersion
	cm.currentConfig.CurrentNodeVersion = version
	logging.Info("Setting current node version", types.Config, "oldVersion", oldVersion, "newVersion", version)
	return nil
}

// SyncVersionFromChain queries the current version from chain and updates config if needed
// This should be called when the blockchain is ready and connections are stable
func (cm *ConfigManager) SyncVersionFromChain(cosmosClient CosmosQueryClient) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := cosmosClient.MLNodeVersion(ctx, &types.QueryGetMLNodeVersionRequest{})
	if err != nil {
		logging.Warn("Failed to sync MLNode version from chain, keeping current version",
			types.Config, "error", err)
		return err
	}

	chainVersion := resp.MlnodeVersion.CurrentVersion
	if chainVersion == "" {
		logging.Warn("Chain version is empty", types.Config)
	}

	currentVersion := cm.GetCurrentNodeVersion()
	if chainVersion != currentVersion {
		logging.Info("Version mismatch detected - updating from chain", types.Config,
			"currentVersion", currentVersion, "chainVersion", chainVersion)
		return cm.SetCurrentNodeVersion(chainVersion)
	}

	logging.Info("Version sync complete - no changes needed", types.Config, "version", currentVersion)
	return nil
}

// CosmosQueryClient defines interface for querying version from cosmos
type CosmosQueryClient interface {
	MLNodeVersion(ctx context.Context, req *types.QueryGetMLNodeVersionRequest, opts ...grpc.CallOption) (*types.QueryGetMLNodeVersionResponse, error)
}

func (cm *ConfigManager) SetValidationParams(params ValidationParamsCache) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.ValidationParams = params
	logging.Info("Setting validation params", types.Config, "params", params)
	return nil
}

func (cm *ConfigManager) GetValidationParams() ValidationParamsCache {
	return cm.currentConfig.ValidationParams
}

func (cm *ConfigManager) SetBandwidthParams(params BandwidthParamsCache) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.BandwidthParams = params
	logging.Info("Setting bandwidth params", types.Config, "params", params)
	return nil
}

func (cm *ConfigManager) GetBandwidthParams() BandwidthParamsCache {
	return cm.currentConfig.BandwidthParams
}

func (cm *ConfigManager) GetHeight() int64 {
	return cm.currentConfig.CurrentHeight
}

func (cm *ConfigManager) GetLastUsedVersion() string {
	return cm.currentConfig.LastUsedVersion
}

func (cm *ConfigManager) SetLastUsedVersion(version string) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.LastUsedVersion = version
	logging.Info("Setting last used version", types.Config, "version", version)
	return nil
}

func (cm *ConfigManager) ShouldRefreshClients() bool {
	currentVersion := cm.GetCurrentNodeVersion()
	lastUsedVersion := cm.GetLastUsedVersion()
	return currentVersion != lastUsedVersion
}

func (cm *ConfigManager) SetPreviousSeed(seed SeedInfo) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.PreviousSeed = seed
	logging.Info("Setting previous seed", types.Config, "seed", seed)
	return nil
}

func (cm *ConfigManager) AdvanceCurrentSeed() {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	cm.currentConfig.PreviousSeed = cm.currentConfig.CurrentSeed
	cm.currentConfig.CurrentSeed = cm.currentConfig.UpcomingSeed
	cm.currentConfig.UpcomingSeed = SeedInfo{}
}

func (cm *ConfigManager) MarkPreviousSeedClaimed() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	prev := cm.currentConfig.PreviousSeed
	prev.Claimed = true
	cm.currentConfig.PreviousSeed = prev
	logging.Info("Marking previous seed as claimed", types.Config, "epochIndex", cm.currentConfig.PreviousSeed.EpochIndex)
	return nil
}

func (cm *ConfigManager) IsPreviousSeedClaimed() bool {
	seed := cm.GetPreviousSeed()
	return seed.Claimed
}

func (cm *ConfigManager) GetPreviousSeed() SeedInfo {
	return cm.currentConfig.PreviousSeed
}

func (cm *ConfigManager) SetCurrentSeed(seed SeedInfo) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.CurrentSeed = seed
	logging.Info("Setting current seed", types.Config, "seed", seed)
	return nil
}

func (cm *ConfigManager) GetCurrentSeed() SeedInfo {
	return cm.currentConfig.CurrentSeed
}

func (cm *ConfigManager) SetUpcomingSeed(seed SeedInfo) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.UpcomingSeed = seed
	logging.Info("Setting upcoming seed", types.Config, "seed", seed)
	return nil
}

func (cm *ConfigManager) GetUpcomingSeed() SeedInfo {
	return cm.currentConfig.UpcomingSeed
}

// Called from:
// 1. syncNodesWithConfig periodic routine
// 2. admin API when nodes are added/removed
func (cm *ConfigManager) SetNodes(nodes []InferenceNodeConfig) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.currentConfig.Nodes = nodes
	logging.Info("Setting nodes", types.Config, "nodes", nodes)
	return nil
}

func (cm *ConfigManager) CreateWorkerKey() (string, error) {
	workerKey := ed25519.GenPrivKey()
	workerPublicKey := workerKey.PubKey()
	workerPublicKeyString := base64.StdEncoding.EncodeToString(workerPublicKey.Bytes())
	workerPrivateKey := workerKey.Bytes()
	workerPrivateKeyString := base64.StdEncoding.EncodeToString(workerPrivateKey)
	cfg := MLNodeKeyConfig{WorkerPublicKey: workerPublicKeyString, WorkerPrivateKey: workerPrivateKeyString}
	cm.currentConfig.MLNodeKeyConfig = cfg
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

func getSqlitePath() string {
	path := os.Getenv("API_SQLITE_PATH")
	if path == "" {
		return "/root/.dapi/gonka.db"
	}
	return path
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

	return config, nil
}

func writeConfig(config Config, writerProvider WriteCloserProvider) error {
	// Skip writing in tests where WriterProvider is nil
	if writerProvider == nil {
		return nil
	}

	writer := writerProvider.GetWriter()
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

// LoadNodeConfig loads additional nodes from a JSON file and writes them into the DB once.
// Idempotent via KV flag kvKeyNodeConfigMerged.
func (cm *ConfigManager) LoadNodeConfig(ctx context.Context, nodeConfigPathOverride string) error {
	if err := cm.ensureDbReady(ctx); err != nil {
		return err
	}

	// If already merged, skip
	var merged bool
	if ok, err := KVGetJSON(ctx, cm.sqlDb.GetDb(), kvKeyNodeConfigMerged, &merged); err == nil && ok && merged {
		logging.Info("Node config already merged. Skipping", types.Config)
		return nil
	}

	nodeConfigPath := nodeConfigPathOverride
	if strings.TrimSpace(nodeConfigPath) == "" {
		var found bool
		nodeConfigPath, found = os.LookupEnv("NODE_CONFIG_PATH")
		if !found {
			nodeConfigPath = ""
		}
	}
	if strings.TrimSpace(nodeConfigPath) == "" {
		logging.Info("NODE_CONFIG_PATH not set. No additional nodes will be added to config", types.Config)
		return nil
	}

	logging.Info("Loading and merging node configuration", types.Config, "path", nodeConfigPath)

	newNodes, err := parseInferenceNodesFromNodeConfigJson(nodeConfigPath)
	if err != nil {
		return err
	}

	// Validate nodes and filter out invalid ones
	validNodes := getValidNodes(newNodes)

	if len(validNodes) < len(newNodes) {
		logging.Warn("Some nodes were skipped due to validation errors", types.Config,
			"total_nodes", len(newNodes), "valid_nodes", len(validNodes), "skipped", len(newNodes)-len(validNodes))
	}

	// Populate in-memory nodes and mark dirty. Auto-flush will persist and then set merged flag.
	cm.mutex.Lock()
	cm.currentConfig.Nodes = validNodes
	cm.mutex.Unlock()

	logging.Info("Loaded node configuration into memory; will persist on next flush", types.Config,
		"valid_nodes", len(validNodes))
	return nil
}

func getValidNodes(newNodes []InferenceNodeConfig) []InferenceNodeConfig {
	validNodes := make([]InferenceNodeConfig, 0, len(newNodes))
	idSet := make(map[string]bool)
	hostPortSet := make(map[string]bool)
	for i, node := range newNodes {
		id := node.Id
		if _, exists := idSet[id]; exists {
			logging.Error("Skipping duplicate node from node_config.json", types.Config, "node_id", id, "index_in_file", i)
			continue
		}

		hostInfPort := strings.ToLower(strings.TrimSpace(node.Host)) + ":" + strconv.Itoa(node.InferencePort)
		if _, exists := hostPortSet[hostInfPort]; exists {
			logging.Error("Skipping node with duplicate host:port from node_config.json", types.Config, "host_port", hostInfPort, "index_in_file", i)
			continue
		}

		hostPocPort := strings.ToLower(strings.TrimSpace(node.Host)) + ":" + strconv.Itoa(node.PoCPort)
		if _, exists := hostPortSet[hostPocPort]; exists {
			logging.Error("Skipping node with duplicate host:port from node_config.json", types.Config, "host_port", hostPocPort, "index_in_file", i)
			continue
		}

		validationErrors := ValidateInferenceNodeBasic(node)

		if len(validationErrors) > 0 {
			logging.Error("Skipping invalid node from node_config.json", types.Config,
				"index", i, "node_id", node.Id, "errors", validationErrors)
		} else {
			validNodes = append(validNodes, node)
			idSet[id] = true
			hostPortSet[hostInfPort] = true
			hostPortSet[hostPocPort] = true
		}
	}
	return validNodes
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

func (cm *ConfigManager) migrateDynamicDataToDb(ctx context.Context) error {
	if err := cm.ensureDbReady(ctx); err != nil {
		return err
	}
	// Only migrate once, gated by a KV flag
	var migrated bool
	if ok, err := KVGetJSON(ctx, cm.sqlDb.GetDb(), kvKeyConfigMigrated, &migrated); err == nil && ok && migrated {
		logging.Info("Config migration already completed. Skipping", types.Config)
		return nil
	}
	config := cm.currentConfig
	// If YAML indicates nodes were already merged historically, persist the flag so LoadNodeConfig skips
	if config.NodeConfigIsMerged {
		_ = KVSetJSON(ctx, cm.sqlDb.GetDb(), kvKeyNodeConfigMerged, true)
	}
	// Nodes: upsert unconditionally (idempotent)
	if err := WriteNodes(ctx, cm.sqlDb.GetDb(), config.Nodes); err != nil {
		logging.Error("Error writing nodes to DB", types.Config, "error", err)
		return err
	}

	// Per-key idempotent migrations: only populate if missing
	// Heights
	if _, ok, _ := KVGetInt64(ctx, cm.sqlDb.GetDb(), kvKeyCurrentHeight); !ok && config.CurrentHeight != 0 {
		_ = KVSetInt64(ctx, cm.sqlDb.GetDb(), kvKeyCurrentHeight, config.CurrentHeight)
	}
	if _, ok, _ := KVGetInt64(ctx, cm.sqlDb.GetDb(), kvKeyLastProcessedHeight); !ok && config.LastProcessedHeight != 0 {
		_ = KVSetInt64(ctx, cm.sqlDb.GetDb(), kvKeyLastProcessedHeight, config.LastProcessedHeight)
	}

	// Seeds (migrate once into typed table if not already present)
	if s := config.CurrentSeed; s.Seed != 0 || s.Signature != "" {
		_ = SetActiveSeed(ctx, cm.sqlDb.GetDb(), "current", s)
	}
	if s := config.PreviousSeed; s.Seed != 0 || s.Signature != "" {
		_ = SetActiveSeed(ctx, cm.sqlDb.GetDb(), "previous", s)
	}
	if s := config.UpcomingSeed; s.Seed != 0 || s.Signature != "" {
		_ = SetActiveSeed(ctx, cm.sqlDb.GetDb(), "upcoming", s)
	}

	// Upgrade plan
	var up UpgradePlan
	if ok, _ := func() (bool, error) { return KVGetJSON(ctx, cm.sqlDb.GetDb(), kvKeyUpgradePlan, &up) }(); !ok && (config.UpgradePlan.Height != 0 || config.UpgradePlan.Name != "") {
		_ = KVSetJSON(ctx, cm.sqlDb.GetDb(), kvKeyUpgradePlan, config.UpgradePlan)
	}

	// Versions
	if _, ok, _ := KVGetString(ctx, cm.sqlDb.GetDb(), kvKeyCurrentNodeVersion); !ok && config.CurrentNodeVersion != "" {
		_ = KVSetString(ctx, cm.sqlDb.GetDb(), kvKeyCurrentNodeVersion, config.CurrentNodeVersion)
	}
	if _, ok, _ := KVGetString(ctx, cm.sqlDb.GetDb(), kvKeyLastUsedVersion); !ok && config.LastUsedVersion != "" {
		_ = KVSetString(ctx, cm.sqlDb.GetDb(), kvKeyLastUsedVersion, config.LastUsedVersion)
	}

	// Params
	var vp ValidationParamsCache
	if ok, _ := func() (bool, error) { return KVGetJSON(ctx, cm.sqlDb.GetDb(), kvKeyValidationParams, &vp) }(); !ok && (config.ValidationParams.TimestampExpiration != 0 || config.ValidationParams.ExpirationBlocks != 0) {
		_ = KVSetJSON(ctx, cm.sqlDb.GetDb(), kvKeyValidationParams, config.ValidationParams)
	}
	var bp BandwidthParamsCache
	if ok, _ := func() (bool, error) { return KVGetJSON(ctx, cm.sqlDb.GetDb(), kvKeyBandwidthParams, &bp) }(); !ok && (config.BandwidthParams.EstimatedLimitsPerBlockKb != 0) {
		_ = KVSetJSON(ctx, cm.sqlDb.GetDb(), kvKeyBandwidthParams, config.BandwidthParams)
	}

	// ML node key config
	var mk MLNodeKeyConfig
	if ok, _ := func() (bool, error) { return KVGetJSON(ctx, cm.sqlDb.GetDb(), kvKeyMLNodeKeyConfig, &mk) }(); !ok && (config.MLNodeKeyConfig.WorkerPublicKey != "" || config.MLNodeKeyConfig.WorkerPrivateKey != "") {
		_ = KVSetJSON(ctx, cm.sqlDb.GetDb(), kvKeyMLNodeKeyConfig, config.MLNodeKeyConfig)
	}

	// Mark migration as done
	_ = KVSetJSON(ctx, cm.sqlDb.GetDb(), kvKeyConfigMigrated, true)
	return nil
}

// HydrateFromDB loads dynamic fields from DB into memory ONCE during startup.
func (cm *ConfigManager) HydrateFromDB(_ context.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = cm.ensureDbReady(ctx)
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	if db := cm.sqlDb.GetDb(); db != nil {
		if nodes, err := ReadNodes(ctx, db); err == nil && len(nodes) >= 0 {
			logging.Info("Reading nodes from DB", types.Config, "nodes", nodes)
			cm.currentConfig.Nodes = nodes
		}
		if s, ok, err := GetActiveSeed(ctx, db, "current"); err == nil && ok {
			cm.currentConfig.CurrentSeed = s
			sanitizedS := s
			sanitizedS.Seed = 0
			logging.Info("Reading active seed from DB", types.Config, "sanitizedSeed", s)
		}
		if s, ok, err := GetActiveSeed(ctx, db, "previous"); err == nil && ok {
			cm.currentConfig.PreviousSeed = s
			sanitizedS := s
			sanitizedS.Seed = 0
			logging.Info("Reading previous seed from DB", types.Config, "sanitizedSeed", s)
		}
		if s, ok, err := GetActiveSeed(ctx, db, "upcoming"); err == nil && ok {
			cm.currentConfig.UpcomingSeed = s
			sanitizedS := s
			sanitizedS.Seed = 0
			logging.Info("Reading upcoming seed from DB", types.Config, "sanitizedSeed", s)
		}
		if v, ok, err := KVGetInt64(ctx, db, kvKeyCurrentHeight); err == nil && ok {
			logging.Info("Reading current height from DB", types.Config, "height", v)
			cm.currentConfig.CurrentHeight = v
		}
		if v, ok, err := KVGetInt64(ctx, db, kvKeyLastProcessedHeight); err == nil && ok {
			logging.Info("Reading last processed height from DB", types.Config, "height", v)
			cm.currentConfig.LastProcessedHeight = v
		}
		var up UpgradePlan
		if ok, err := KVGetJSON(ctx, db, kvKeyUpgradePlan, &up); err == nil && ok {
			logging.Info("Reading upgrade plan from DB", types.Config, "plan", up)
			cm.currentConfig.UpgradePlan = up
		}
		if v, ok, err := KVGetString(ctx, db, kvKeyCurrentNodeVersion); err == nil && ok {
			logging.Info("Reading current node version from DB", types.Config, "version", v)
			cm.currentConfig.CurrentNodeVersion = v
		}
		if v, ok, err := KVGetString(ctx, db, kvKeyLastUsedVersion); err == nil && ok {
			logging.Info("Reading last used version from DB", types.Config, "version", v)
			cm.currentConfig.LastUsedVersion = v
		}
		var vp ValidationParamsCache
		if ok, err := KVGetJSON(ctx, db, kvKeyValidationParams, &vp); err == nil && ok {
			logging.Info("Reading validation params from DB", types.Config, "params", vp)
			cm.currentConfig.ValidationParams = vp
		}
		var bp BandwidthParamsCache
		if ok, err := KVGetJSON(ctx, db, kvKeyBandwidthParams, &bp); err == nil && ok {
			logging.Info("Reading bandwidth params from DB", types.Config, "params", bp)
			cm.currentConfig.BandwidthParams = bp
		}
		var mk MLNodeKeyConfig
		if ok, err := KVGetJSON(ctx, db, kvKeyMLNodeKeyConfig, &mk); err == nil && ok {
			cm.currentConfig.MLNodeKeyConfig = mk
			sanitizedMk := mk
			mk.WorkerPrivateKey = ""
			logging.Info("Reading MLNodeKeyConfig from DB", types.Config, "sanitizedConfig", sanitizedMk)
		}
	}
	return nil
}

// StartAutoFlush launches a background goroutine that periodically flushes dynamic fields to DB.
func (cm *ConfigManager) StartAutoFlush(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = cm.flushToDB(ctx)
			}
		}
	}()
}

// FlushNow flushes dynamic fields immediately.
func (cm *ConfigManager) FlushNow(ctx context.Context) error {
	logging.Info("Executing FlushNow", types.Config)
	return cm.flushToDB(ctx)
}

// flushToDB writes all dynamic fields if there were any changes since last flush.
func (cm *ConfigManager) flushToDB(ctx context.Context) error {
	logging.Info("Executing flushToDB", types.Config)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := cm.ensureDbReady(ctx); err != nil {
		return err
	}
	cm.mutex.Lock()
	cfg := cm.currentConfig
	cm.mutex.Unlock()
	db := cm.sqlDb.GetDb()
	if db == nil {
		return nil
	}

	// Always flush everything; each logical group uses its own transaction inside helpers
	if err := ReplaceInferenceNodes(ctx, db, cfg.Nodes); err != nil {
		return err
	}
	_ = KVSetJSON(ctx, db, kvKeyNodeConfigMerged, true)

	// Seeds: must be atomic as a group; perform in one tx
	if err := setSeedsAtomic(ctx, db, cfg); err != nil {
		return err
	}

	_ = KVSetInt64(ctx, db, kvKeyCurrentHeight, cfg.CurrentHeight)
	_ = KVSetInt64(ctx, db, kvKeyLastProcessedHeight, cfg.LastProcessedHeight)
	_ = KVSetJSON(ctx, db, kvKeyUpgradePlan, cfg.UpgradePlan)
	_ = KVSetString(ctx, db, kvKeyCurrentNodeVersion, cfg.CurrentNodeVersion)
	_ = KVSetString(ctx, db, kvKeyLastUsedVersion, cfg.LastUsedVersion)
	_ = KVSetJSON(ctx, db, kvKeyMLNodeKeyConfig, cfg.MLNodeKeyConfig)
	_ = KVSetJSON(ctx, db, kvKeyValidationParams, cfg.ValidationParams)
	_ = KVSetJSON(ctx, db, kvKeyBandwidthParams, cfg.BandwidthParams)

	logging.Info("Flushed dynamic config to DB", types.Config)

	// Also write a pretty-printed config dump JSON next to the DB
	if cm.configDumpPath != "" {
		if dumpBytes, err := json.MarshalIndent(cfg, "", "  "); err != nil {
			logging.Warn("Failed to marshal config dump", types.Config, "error", err)
		} else {
			// Ensure directory exists
			_ = os.MkdirAll(filepath.Dir(cm.configDumpPath), 0o755)
			if err := os.WriteFile(cm.configDumpPath, dumpBytes, 0o644); err != nil {
				logging.Warn("Failed to write config dump", types.Config, "path", cm.configDumpPath, "error", err)
			}
			logging.Info("Saved config dump", types.Config, "configDumpPath", cm.configDumpPath)
		}
	}
	return nil
}

// setSeedsAtomic writes all three seeds in a single transaction to keep them consistent.
func setSeedsAtomic(ctx context.Context, db *sql.DB, cfg Config) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE seed_info SET is_active = 0 WHERE is_active = 1 AND type IN ('current','previous','upcoming')`); err != nil {
		return err
	}
	if err := insertSeedTx(ctx, tx, "current", cfg.CurrentSeed); err != nil {
		return err
	}
	if err := insertSeedTx(ctx, tx, "previous", cfg.PreviousSeed); err != nil {
		return err
	}
	if err := insertSeedTx(ctx, tx, "upcoming", cfg.UpcomingSeed); err != nil {
		return err
	}
	return tx.Commit()
}

func insertSeedTx(ctx context.Context, tx *sql.Tx, seedType string, s SeedInfo) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO seed_info(type, seed, epoch_index, signature, claimed, is_active) VALUES(?, ?, ?, ?, ?, 1)`,
		seedType, s.Seed, s.EpochIndex, s.Signature, s.Claimed)
	return err
}

// ensureDbReady pings the DB and attempts to reopen if needed
func (cm *ConfigManager) ensureDbReady(ctx context.Context) error {
	db := cm.sqlDb.GetDb()
	if db != nil {
		if err := db.PingContext(ctx); err == nil {
			return nil
		}
	}
	// Reopen
	reopenPath := cm.sqlitePath
	if strings.TrimSpace(reopenPath) == "" {
		reopenPath = getSqlitePath()
	}
	newDb := NewSQLiteDb(SqliteConfig{Path: reopenPath})
	if err := newDb.BootstrapLocal(ctx); err != nil {
		return err
	}
	// Close old handle to avoid leaks
	if cm.sqlDb != nil && cm.sqlDb.GetDb() != nil {
		_ = cm.sqlDb.GetDb().Close()
	}
	cm.sqlDb = newDb
	return nil
}

// getStaticConfigCopyUnsafe returns a copy of config with dynamic fields zeroed for file persistence.
func (cm *ConfigManager) getStaticConfigCopyUnsafe() Config {
	c := cm.currentConfig
	// Zero dynamic fields
	c.Nodes = nil
	c.NodeConfigIsMerged = false
	c.UpcomingSeed = SeedInfo{}
	c.CurrentSeed = SeedInfo{}
	c.PreviousSeed = SeedInfo{}
	c.CurrentHeight = 0
	c.LastProcessedHeight = 0
	c.UpgradePlan = UpgradePlan{}
	c.MLNodeKeyConfig = MLNodeKeyConfig{}
	c.CurrentNodeVersion = ""
	c.LastUsedVersion = ""
	c.ValidationParams = ValidationParamsCache{}
	c.BandwidthParams = BandwidthParamsCache{}
	return c
}

// KV keys for dynamic data
const (
	kvKeyCurrentHeight       = "current_height"
	kvKeyLastProcessedHeight = "last_processed_height"
	kvKeyUpgradePlan         = "upgrade_plan"
	kvKeyCurrentSeed         = "seed_current"
	kvKeyPreviousSeed        = "seed_previous"
	kvKeyUpcomingSeed        = "seed_upcoming"
	kvKeyCurrentNodeVersion  = "current_node_version"
	kvKeyLastUsedVersion     = "last_used_version"
	kvKeyValidationParams    = "validation_params"
	kvKeyBandwidthParams     = "bandwidth_params"
	kvKeyMLNodeKeyConfig     = "ml_node_key_config"
	kvKeyNodeConfigMerged    = "node_config_merged"
	kvKeyConfigMigrated      = "config_migrated"
)
