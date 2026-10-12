package testutil

import "devshard/types"

// RuntimeTestVersion is the protocol / session bind tag used in tests
// (CreateSessionParams.Version, host boundVersion, state-root version).
//
// It tracks EffectiveStateRootAndProtocolVersion so tests stay consistent when
// the default protocol constant moves (e.g. merge onto gateway-v4 where it is
// "v4") and when binaries are link-stamped via DEVSHARD_VERSION.
var RuntimeTestVersion = types.EffectiveStateRootAndProtocolVersion
