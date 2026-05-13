package state

import "devshard/types"

// useV2StateRootComposition selects the Phase 1 state-root composition
// (sealed accumulator + live inference set) for this binary. It is a
// compile-time constant: the runtime carries no "v1 vs v2" dispatch on a
// session field, because each binary release ships a single composition. A
// future Phase 1 release will flip this to true and stamp a different value
// into EscrowState.Version / SettlementPayload.Version so chain verifiers
// dispatch the correct hash path.
//
// StateMachine.testV2Composition (same package, tests only) ORs with this
// const so Phase 1.2/1.3 behavior can be exercised without flipping the global
// release flag.
const useV2StateRootComposition = false

// normalizeBoundVersion returns the supplied binary tag, falling back to
// types.DefaultStateRootVersion when the input is empty. Used at the few
// boundary points (snapshot load, settlement payload assembly, recovery)
// where the field may be absent because of a legacy record.
func normalizeBoundVersion(version string) string {
	if version == "" {
		return types.DefaultStateRootVersion
	}
	return version
}
