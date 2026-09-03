// Binary gencompose renders docker-compose.yml from testenv config.yaml.
//
// Phase 4 stub: fills host/user/warm keys, syncs mock-chain seed fields, writes
// config.yaml back, emits mock-chain-only compose. Phase 6 extends the template.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"devshard/signing"
	"devshard/testenv/config"
	"devshard/testenv/keymaterial"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "input config YAML (created if missing)")
	outPath := flag.String("out", "docker-compose.yml", "output docker-compose file")
	flag.Parse()

	cfg, err := loadOrDefault(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := fillConfig(cfg); err != nil {
		log.Fatalf("fill config: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("validate config: %v", err)
	}

	workDir := filepath.Dir(*outPath)
	if workDir == "." {
		workDir, _ = os.Getwd()
	}
	keyringDir := filepath.Join(workDir, "keyring")
	if err := materializeKeyrings(cfg, keyringDir); err != nil {
		log.Fatalf("materialize keyrings: %v", err)
	}

	if err := writeCompose(cfg, *outPath); err != nil {
		log.Fatalf("write docker-compose: %v", err)
	}

	envPath := filepath.Join(filepath.Dir(*outPath), ".env")
	if err := writeEnvFile(cfg, envPath); err != nil {
		log.Fatalf("write .env: %v", err)
	}

	if err := cfg.Save(*cfgPath); err != nil {
		log.Fatalf("save config: %v", err)
	}

	log.Printf("wrote %s, %s, and updated %s", *outPath, envPath, *cfgPath)
	log.Printf("chain: %s  hosts: %d  escrow slots: %d", cfg.ChainID, len(cfg.Hosts), cfg.Escrow.Slots)
	log.Printf("start: docker compose up -d")
}

func loadOrDefault(path string) (*config.File, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if !os.IsNotExist(err) && !strings.Contains(err.Error(), "no such file") {
		return nil, err
	}
	log.Printf("config not found at %s, starting from defaults", path)
	return defaultConfig(), nil
}

func defaultConfig() *config.File {
	cfg := &config.File{}
	cfg.Hosts = []config.HostCfg{
		{ID: "versiond-0"},
		{ID: "versiond-1"},
		{ID: "versiond-2"},
	}
	cfg.ApplyDefaults()
	return cfg
}

func fillConfig(cfg *config.File) error {
	cfg.ApplyDefaults()
	applyVersiondMode(cfg)
	if err := generateHosts(cfg); err != nil {
		return fmt.Errorf("hosts: %w", err)
	}
	if err := generateUser(cfg); err != nil {
		return fmt.Errorf("user: %w", err)
	}
	if err := generateWarmGrantee(cfg); err != nil {
		return fmt.Errorf("warm_grantee: %w", err)
	}
	stampKeyNames(cfg)
	assignSlots(cfg)
	fillNetworkDefaults(cfg)
	syncChainSeed(cfg)
	return nil
}

// stampKeyNames writes the resolved KEY_NAME onto every host so the generated
// config states its identity groupings instead of leaving them implied by
// roster position. Names are resolved before any assignment: hosts[1] must
// still see the pre-stamp roster to inherit hosts[0]'s key.
func stampKeyNames(cfg *config.File) {
	if cfg == nil {
		return
	}
	names := make([]string, len(cfg.Hosts))
	for i, h := range cfg.Hosts {
		names[i] = config.VersiondKeyName(cfg, h)
	}
	for i := range cfg.Hosts {
		if names[i] != "" {
			cfg.Hosts[i].KeyName = names[i]
		}
	}
}

func generateHosts(cfg *config.File) error {
	for i := range cfg.Hosts {
		h := &cfg.Hosts[i]
		if isPlaceholderKey(h.PrivateKeyHex) {
			signer, err := signing.GenerateKey()
			if err != nil {
				return fmt.Errorf("generate host %d key: %w", i, err)
			}
			h.PrivateKeyHex = signer.PrivateKeyHex()
			h.Address = signer.Address()
		} else if h.Address == "" {
			signer, err := signing.SignerFromHex(h.PrivateKeyHex)
			if err != nil {
				return fmt.Errorf("parse host %d key: %w", i, err)
			}
			h.Address = signer.Address()
		}
		if h.ID == "" {
			h.ID = fmt.Sprintf("versiond-%d", i)
		}
		if h.Port == 0 {
			h.Port = config.DefaultHostPort
		}
	}
	return nil
}

func generateUser(cfg *config.File) error {
	if isPlaceholderKey(cfg.User.PrivateKeyHex) {
		signer, err := signing.GenerateKey()
		if err != nil {
			return fmt.Errorf("generate user key: %w", err)
		}
		cfg.User.PrivateKeyHex = signer.PrivateKeyHex()
		cfg.User.Address = signer.Address()
	} else if cfg.User.Address == "" {
		signer, err := signing.SignerFromHex(cfg.User.PrivateKeyHex)
		if err != nil {
			return fmt.Errorf("parse user key: %w", err)
		}
		cfg.User.Address = signer.Address()
	}
	if cfg.User.Port == 0 {
		cfg.User.Port = config.DefaultUserPort
	}
	return nil
}

func generateWarmGrantee(cfg *config.File) error {
	w := &cfg.WarmGrantee
	if isPlaceholderKey(w.PrivateKeyHex) {
		signer, err := signing.GenerateKey()
		if err != nil {
			return fmt.Errorf("generate warm grantee key: %w", err)
		}
		w.PrivateKeyHex = signer.PrivateKeyHex()
		w.Address = signer.Address()
	} else if w.Address == "" && w.PrivateKeyHex != "" {
		signer, err := signing.SignerFromHex(w.PrivateKeyHex)
		if err != nil {
			return fmt.Errorf("parse warm grantee key: %w", err)
		}
		w.Address = signer.Address()
	}
	return nil
}

func assignSlots(cfg *config.File) {
	for i := range cfg.Hosts {
		cfg.Hosts[i].SlotIDs = nil
	}
	n := len(cfg.Hosts)
	if n == 0 {
		return
	}
	if cfg.Versiond.Mode == config.VersiondModeMulti {
		// Round-robin slots across distinct KEY_NAME identities (replicas of
		// one identity own no slots of their own) so solos execute and the
		// replicated participant validates — lease exclusivity needs this.
		// A single identity owns every slot: chat works, but the executor
		// never validates its own work (no lease races).
		var identities []int
		seen := make(map[string]struct{}, n)
		for i, h := range cfg.Hosts {
			key := config.VersiondKeyName(cfg, h)
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			identities = append(identities, i)
		}
		if len(identities) == 0 {
			return
		}
		for slot := range cfg.Escrow.Slots {
			idx := identities[slot%len(identities)]
			cfg.Hosts[idx].SlotIDs = append(cfg.Hosts[idx].SlotIDs, slot)
		}
		return
	}
	for slot := range cfg.Escrow.Slots {
		idx := slot % n
		cfg.Hosts[idx].SlotIDs = append(cfg.Hosts[idx].SlotIDs, slot)
	}
}

func applyVersiondMode(cfg *config.File) {
	switch cfg.Versiond.Mode {
	case config.VersiondModeSingle:
		if len(cfg.Hosts) > 1 {
			cfg.Hosts = cfg.Hosts[:1]
		}
		cfg.Postgres.Enabled = false
	case config.VersiondModeMulti:
		if len(cfg.Hosts) < 2 {
			for len(cfg.Hosts) < 3 {
				cfg.Hosts = append(cfg.Hosts, config.HostCfg{
					ID: fmt.Sprintf("versiond-%d", len(cfg.Hosts)),
				})
			}
		}
		cfg.Postgres.Enabled = true
	}
}

func fillNetworkDefaults(cfg *config.File) {
	base := cfg.Network.BaseIP
	if base == "" {
		base = config.DefaultNetworkBaseIP
	}
	if cfg.VersiondRouter.IP == "" {
		cfg.VersiondRouter.IP = fmt.Sprintf("%s.5", base)
	}
	if cfg.Devshardctl.IP == "" {
		cfg.Devshardctl.IP = fmt.Sprintf("%s.6", base)
	}
	if cfg.Postgres.Enabled && cfg.Postgres.IP == "" {
		cfg.Postgres.IP = fmt.Sprintf("%s.7", base)
	}
	for i := range cfg.Hosts {
		h := &cfg.Hosts[i]
		if h.IP == "" {
			h.IP = fmt.Sprintf("%s.%d", base, 10+i)
		}
		if h.URL == "" {
			h.URL = config.RouterBaseURL(cfg)
		}
	}
}

// syncChainSeed rewrites chain-seed fields from filled host/user keys so mock-chain
// and compose agree on identities without manual bech32 editing.
func syncChainSeed(cfg *config.File) {
	routerURL := cfg.Escrow.SlotURL
	if routerURL == "" {
		routerURL = config.RouterBaseURL(cfg)
	}
	cfg.Escrow.SlotURL = routerURL

	slots := make([]string, cfg.Escrow.Slots)
	for _, h := range cfg.Hosts {
		for _, sid := range h.SlotIDs {
			if sid >= 0 && sid < len(slots) && h.Address != "" {
				slots[sid] = h.Address
			}
		}
	}
	// Fill any gaps (should not happen after assignSlots). Multi mode must
	// land on an on-chain identity, never on a replica that owns no key.
	n := len(cfg.Hosts)
	identities := config.OnChainIdentityHosts(cfg)
	for i := range slots {
		if slots[i] != "" || n == 0 {
			continue
		}
		if cfg.Versiond.Mode == config.VersiondModeMulti {
			if len(identities) == 0 {
				continue
			}
			slots[i] = identities[i%len(identities)].Address
			continue
		}
		slots[i] = cfg.Hosts[i%n].Address
	}

	// Distinct on-chain participants: one per KEY_NAME. Hosts sharing a
	// KEY_NAME are replicas, not extra identities. Solos keep their own key
	// and a direct InferenceURL so gateway/gossip bypass the sticky pool.
	participants := make([]config.Participant, 0, len(cfg.Hosts))
	for _, h := range config.OnChainIdentityHosts(cfg) {
		if h.Address == "" {
			continue
		}
		url := routerURL
		if cfg.Versiond.Mode == config.VersiondModeMulti && !config.IsRouterPooledHost(cfg, h) {
			url = fmt.Sprintf("http://%s:%d", h.ID, config.DefaultHostPort)
		}
		participants = append(participants, config.Participant{
			Address:      h.Address,
			InferenceURL: url,
		})
	}
	cfg.Participants = participants

	if len(cfg.Escrows) == 0 {
		cfg.Escrows = []config.Escrow{{ID: 1}}
	}
	e := &cfg.Escrows[0]
	if e.ID == 0 {
		e.ID = 1
	}
	e.Creator = cfg.User.Address
	e.Slots = slots
	if e.Amount == 0 {
		e.Amount = config.DefaultEscrowAmount
	}
	if e.EpochIndex == 0 {
		e.EpochIndex = cfg.Epoch.Index
	}
	if e.AppHash == "" {
		e.AppHash = config.DefaultAppHash
	}
	if e.ModelID == "" {
		e.ModelID = config.DefaultModelID
	}
	if e.TokenPrice == 0 {
		e.TokenPrice = config.DefaultTokenPrice
	}
	if e.ValidationRate == 0 {
		e.ValidationRate = cfg.Params.ValidationRate
	}
	if e.VoteThresholdFactor == 0 {
		e.VoteThresholdFactor = cfg.Params.VoteThresholdFactor
	}

	if len(cfg.Hosts) > 0 && cfg.Hosts[0].Address != "" {
		grantees := []string{}
		if cfg.WarmGrantee.Address != "" {
			grantees = []string{cfg.WarmGrantee.Address}
		}
		cfg.Grantees = []config.GranteeBinding{{
			GranterAddress: cfg.Hosts[0].Address,
			MessageTypeURL: "/inference.inference.MsgStartInference",
			Grantees:       grantees,
		}}
	}

	if len(cfg.EpochGroups) == 0 {
		cfg.EpochGroups = []config.EpochGroupBinding{{
			EpochIndex:          cfg.Epoch.Index,
			ModelID:             config.DefaultModelID,
			ValidationThreshold: 50,
		}}
	}
}

func isPlaceholderKey(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	up := strings.ToUpper(t)
	return strings.HasPrefix(up, "TODO") || strings.HasPrefix(up, "CHANGEME")
}

func materializeKeyrings(cfg *config.File, baseDir string) error {
	if err := keymaterial.MaterializeHosts(baseDir, cfg); err != nil {
		return err
	}
	log.Printf("materialized %d file keyring(s) under %s", len(cfg.Hosts), baseDir)
	return nil
}
