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
