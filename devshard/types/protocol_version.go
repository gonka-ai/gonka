// Protocol version link stamps (see devshard/docs/upgrade.md):
//
// buildStateRootProtocolVersion — baked in at build via -X (DEVSHARD_VERSION).
// EffectiveStateRootAndProtocolVersion — resolved in init(): link stamp, or
// DevshardStateRootAndProtocolVersion when the link stamp is "".
package types

import (
	"strconv"
	"strings"
)

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

// RequiresMainnetHeightProof gates the new consensus rule by the escrow's
// frozen protocol version, never by a local runtime switch.
func RequiresMainnetHeightProof(version string) bool {
	parsed, err := ParseProtocolVersion(version)
	if err != nil {
		return false
	}
	major := strings.Split(string(parsed), ".")[0]
	n, err := strconv.ParseUint(major, 10, 32)
	return err == nil && n >= 7
}
