package config

import "testing"

func TestOnChainHostCount_DistinctKeyNames(t *testing.T) {
	cfg := &File{
		Versiond: VersiondCfg{Mode: VersiondModeMulti},
		Hosts: []HostCfg{
			{ID: "versiond-0"},
			{ID: "versiond-1"},
			{ID: "versiond-2"},
			{ID: "versiond-3"},
		},
	}
	if got := cfg.onChainHostCount(); got != 3 {
		t.Fatalf("multi 4 containers: onChainHostCount=%d, want 3 distinct KEY_NAME identities", got)
	}
	if got := VersiondKeyName(cfg, cfg.Hosts[0]); got != "versiond-0" {
		t.Fatalf("hosts[0] KEY_NAME=%q, want versiond-0", got)
	}
	if got := VersiondKeyName(cfg, cfg.Hosts[1]); got != "versiond-0" {
		t.Fatalf("hosts[1] KEY_NAME=%q, want versiond-0 (HA pair)", got)
	}
	if got := VersiondKeyName(cfg, cfg.Hosts[2]); got != "versiond-2" {
		t.Fatalf("hosts[2] KEY_NAME=%q, want versiond-2 (solo)", got)
	}

	cfg.Versiond.Mode = VersiondModeSingle
	if got := cfg.onChainHostCount(); got != 4 {
		t.Fatalf("single mode: onChainHostCount=%d, want 4", got)
	}

	pair := &File{
		Versiond: VersiondCfg{Mode: VersiondModeMulti},
		Hosts: []HostCfg{
			{ID: "versiond-0"},
			{ID: "versiond-1"},
		},
	}
	if got := pair.onChainHostCount(); got != 1 {
		t.Fatalf("HA pair only: onChainHostCount=%d, want 1", got)
	}
}

// R7: identity count follows KEY_NAME groupings, so a second replica of the
// same identity does not inflate the count (positional n-1 would say 3).
func TestOnChainHostCount_ThreeReplicasOfOneIdentity(t *testing.T) {
	cfg := &File{
		Versiond: VersiondCfg{Mode: VersiondModeMulti},
		Hosts: []HostCfg{
			{ID: "versiond-0", KeyName: "versiond-0"},
			{ID: "versiond-1", KeyName: "versiond-0"},
			{ID: "versiond-2", KeyName: "versiond-0"},
			{ID: "versiond-3", KeyName: "versiond-3"},
		},
	}
	if got := cfg.onChainHostCount(); got != 2 {
		t.Fatalf("4 containers / 2 keys: onChainHostCount=%d, want 2", got)
	}
	if got := KeyNameReplicaCount(cfg, cfg.Hosts[2]); got != 3 {
		t.Fatalf("replica count=%d, want 3", got)
	}
	if got := KeyNameReplicaCount(cfg, cfg.Hosts[3]); got != 1 {
		t.Fatalf("solo replica count=%d, want 1", got)
	}
	ids := RouterPoolHostIDs(cfg)
	want := []string{"versiond-0", "versiond-1", "versiond-2"}
	if len(ids) != len(want) {
		t.Fatalf("router pool=%v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("router pool=%v, want %v", ids, want)
		}
	}
	if IsRouterPooledHost(cfg, cfg.Hosts[3]) {
		t.Fatal("a solo identity must be reached directly, not through the sticky pool")
	}
	identities := OnChainIdentityHosts(cfg)
	if len(identities) != 2 || identities[0].ID != "versiond-0" || identities[1].ID != "versiond-3" {
		t.Fatalf("identity hosts=%v, want versiond-0 and versiond-3", identities)
	}
}

// A replica declared out of roster order still resolves to its identity: the
// grouping is key material, not position.
func TestVersiondKeyName_ExplicitKeyNameBeatsPosition(t *testing.T) {
	cfg := &File{
		Versiond: VersiondCfg{Mode: VersiondModeMulti},
		Hosts: []HostCfg{
			{ID: "versiond-0", KeyName: "identity-a"},
			{ID: "versiond-1", KeyName: "identity-b"},
			{ID: "versiond-2", KeyName: "identity-a"},
		},
	}
	if got := VersiondKeyName(cfg, cfg.Hosts[1]); got != "identity-b" {
		t.Fatalf("hosts[1] KEY_NAME=%q, want identity-b (own key despite index 1)", got)
	}
	if got := cfg.onChainHostCount(); got != 2 {
		t.Fatalf("onChainHostCount=%d, want 2", got)
	}
	ids := RouterPoolHostIDs(cfg)
	if len(ids) != 2 || ids[0] != "versiond-0" || ids[1] != "versiond-2" {
		t.Fatalf("router pool=%v, want versiond-0 and versiond-2", ids)
	}
}

// An unkeyed hosts[1] inherits hosts[0]'s declared key, not its container id.
func TestVersiondKeyName_DefaultPairInheritsDeclaredKey(t *testing.T) {
	cfg := &File{
		Versiond: VersiondCfg{Mode: VersiondModeMulti},
		Hosts: []HostCfg{
			{ID: "versiond-0", KeyName: "identity-a"},
			{ID: "versiond-1"},
			{ID: "versiond-2"},
		},
	}
	if got := VersiondKeyName(cfg, cfg.Hosts[1]); got != "identity-a" {
		t.Fatalf("hosts[1] KEY_NAME=%q, want identity-a", got)
	}
	if got := cfg.onChainHostCount(); got != 2 {
		t.Fatalf("onChainHostCount=%d, want 2", got)
	}
}
