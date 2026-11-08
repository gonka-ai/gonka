package gatewayproto

import (
	"errors"
	"strings"

	"devshard/state"
	"devshard/types"
)

// IsLegacyStateRootDivergence preserves the legacy gateway's text profile.
func IsLegacyStateRootDivergence(err error) bool {
	return err != nil && strings.Contains(err.Error(), "post_state_root does not match computed state root")
}

// IsStateRootDivergence preserves the typed and HTTP-wrapped mismatch profile.
func IsStateRootDivergence(err error) bool {
	return state.IsPostStateRootMismatchError(err) || errors.Is(err, types.ErrStateHashMismatch)
}
