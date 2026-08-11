// Protocol version link stamps (see devshard/docs/upgrade.md):
//
// buildStateRootProtocolVersion — baked in at build via -X (DEVSHARD_VERSION).
// EffectiveStateRootAndProtocolVersion — resolved in init(): link stamp, or
// DevshardStateRootAndProtocolVersion when the link stamp is "".
package types

import "strings"

var buildStateRootProtocolVersion string

var EffectiveStateRootAndProtocolVersion string

func init() {
	if v := strings.TrimSpace(buildStateRootProtocolVersion); v != "" {
		EffectiveStateRootAndProtocolVersion = NormalizeVersion(v)
		return
	}
	EffectiveStateRootAndProtocolVersion = DevshardStateRootAndProtocolVersion
}

// BuildStateRootProtocolVersion exposes the raw link-time stamp for tests and tooling.
func BuildStateRootProtocolVersion() string {
	return buildStateRootProtocolVersion
}
