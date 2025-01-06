package apiconfig

import (
	"decentralized-api/broker"
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
