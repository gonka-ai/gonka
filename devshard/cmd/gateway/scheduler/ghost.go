package scheduler

// GhostKind labels why a nonce was burned as a silent ghost probe instead of served.
type GhostKind int

const (
	ghostPoC GhostKind = iota
	ghostWindowFull
	ghostCutOff
	ghostEjected
	ghostNotAllowed
	ghostStateDiverged
	ghostExclude
	ghostAbandoned
	ghostPrepareFailed
	ghostAbsentFromChain
)

func (k GhostKind) reason() string {
	switch k {
	case ghostPoC:
		return GhostReasonPoCUnavailable
	case ghostWindowFull:
		return GhostReasonWindowFull
	case ghostCutOff:
		return GhostReasonCutOff
	case ghostEjected:
		return GhostReasonEjected
	case ghostNotAllowed:
		return GhostReasonOutsideAllowlist
	case ghostStateDiverged:
		return GhostReasonStateDiverged
	case ghostExclude:
		return GhostReasonNoCompatibleRequest
	case ghostAbandoned:
		return GhostReasonAbandoned
	case ghostPrepareFailed:
		return GhostReasonPrepareFailed
	case ghostAbsentFromChain:
		return GhostReasonAbsentFromChain
	default:
		return ""
	}
}
