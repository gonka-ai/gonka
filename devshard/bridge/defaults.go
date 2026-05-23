package bridge

import dstypes "devshard/types"

// DevshardDefaults supplies governance-pinned bind-time grace defaults (same
// fields as GetRuntimeConfig / DevshardEscrowParams). Implementations read from
// dapi cache or devshardd runtimeconfig snapshot; nil on ChainBridge keeps the
// legacy QueryParams path for REST-only clients and tests.
type DevshardDefaults interface {
	DefaultSealGraceNonces() uint32
	DefaultInferenceClearGraceSeconds() uint32
}

// ResolveBindGrace maps module defaults into values frozen on EscrowInfo at bind.
// Zero seal/clear from governance means use devshard canonical fallbacks (group-scaled seal).
func ResolveBindGrace(sealDefault, clearDefault uint32, groupSize int) (sealGrace, clearGrace uint32) {
	sealGrace = sealDefault
	if sealGrace == 0 {
		sealGrace = dstypes.DefaultSealGraceNonces(groupSize)
	}
	clearGrace = clearDefault
	if clearGrace == 0 {
		clearGrace = dstypes.DefaultInferenceClearGraceSeconds
	}
	return sealGrace, clearGrace
}
