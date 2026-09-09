package testutil

import "devshard/types"

// RuntimeTestVersion is the protocol / session bind tag used in tests
// (CreateSessionParams.Version, host boundVersion, state-root version).
//
// It tracks EffectiveStateRootAndProtocolVersion so tests stay consistent when
// the default protocol constant moves (e.g. unstamped fallback "v5") and when
// binaries are link-stamped via DEVSHARD_VERSION.
var RuntimeTestVersion = types.EffectiveStateRootAndProtocolVersion

// TestRoutePrefix is the HTTP mount that matches RuntimeTestVersion. User
// sessions bind SM version from the route prefix; host httptest servers must
// mount the same path so state roots agree.
func TestRoutePrefix() string {
	return "/devshard/" + RuntimeTestVersion
}
