package types

import "strings"

// buildStateRootProtocolVersion is set at link time (see Makefile / Dockerfile
// DEVSHARD_PROTOCOL_VERSION). Empty in plain `go test` / `go run` falls back to
// DevshardStateRootAndProtocolVersion.
var buildStateRootProtocolVersion string

// EffectiveStateRootAndProtocolVersion is the state-root / settlement protocol tag
// for this process. Resolved once in init() from the link-time stamp or
// DevshardStateRootAndProtocolVersion; it does not change at runtime.
// Testermint reads the same value from build/devshard-protocol-version written by
// `make devshardd-build`.
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
