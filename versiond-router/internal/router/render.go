package router

import (
	"fmt"
	"strings"
)

func Render(template []byte, state State) ([]byte, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}

	var ha strings.Builder
	for _, host := range state.Hosts {
		fmt.Fprintf(&ha, "    server %s:%d resolve", host.Address, state.Port)
		if host.State != HostActive {
			ha.WriteString(" down")
		}
		ha.WriteString(";\n")
	}

	legacy := state.Hosts[state.hostIndex(state.LegacyHost)]
	var legacyLine strings.Builder
	fmt.Fprintf(&legacyLine, "    server %s:%d resolve", legacy.Address, state.Port)
	if legacy.State != HostActive {
		legacyLine.WriteString(" down")
	}
	legacyLine.WriteString(";\n")

	var backendMap strings.Builder
	backendMap.WriteString("    default versiond_ha_pool;\n")
	for _, version := range state.NonHAVersions {
		fmt.Fprintf(&backendMap, "    %s versiond_legacy;\n", version)
	}

	var haHeaderMap strings.Builder
	haHeaderMap.WriteString("    default \"\";\n")
	if len(state.Hosts) > 1 {
		haHeaderMap.WriteString("    versiond_ha_pool \"true\";\n")
	}

	replacements := map[string]string{
		"${HA_UPSTREAM_SERVERS}":     strings.TrimSuffix(ha.String(), "\n"),
		"${LEGACY_UPSTREAM_SERVERS}": strings.TrimSuffix(legacyLine.String(), "\n"),
		"${VERSIOND_BACKEND_MAP}":    strings.TrimSuffix(backendMap.String(), "\n"),
		"${DEVSHARD_HA_HEADER_MAP}":  strings.TrimSuffix(haHeaderMap.String(), "\n"),
	}
	out := string(template)
	for placeholder, value := range replacements {
		if !strings.Contains(out, placeholder) {
			return nil, fmt.Errorf("router template missing placeholder %s", placeholder)
		}
		out = strings.ReplaceAll(out, placeholder, value)
	}
	return []byte(fmt.Sprintf("# router-state-generation: %d\n%s", state.Generation, out)), nil
}
