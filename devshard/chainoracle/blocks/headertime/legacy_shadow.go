package headertime

import "time"

// LegacyShadowEpsilon is the one slack the legacy shadow uses on every
// side. A stamp within this of the wall anchor is the same decision.
// It matches the default gateway TimeoutBuffer, so a few seconds of
// send time versus block time stays quiet. Callers do not pass their own.
const LegacyShadowEpsilon = 5 * time.Second

// LegacyShadow is one legacy wall-versus-stamp sample.
type LegacyShadow struct {
	Cause    string
	StampDue bool
	Mismatch bool
}

// CompareLegacyShadow is the only legacy wall/stamp comparison. The gateway
// and the verifier both call it. The stamp deadline uses the same slack as
// the active wall deadline (wallDeadline minus wallAnchor), so a buffer on
// the wall arm is also on the stamp and drops out of the comparison.
// Deadlines within LegacyShadowEpsilon are the same decision. A missing
// stamp is stamp_missing and is not a mismatch. The other causes are
// stamp_early, stamp_late, and both_due.
func CompareLegacyShadow(now, wallAnchor, wallDeadline time.Time, stampMs int64, missing bool) LegacyShadow {
	if missing || stampMs <= 0 {
		return LegacyShadow{Cause: "stamp_missing"}
	}
	stampDeadline := time.UnixMilli(stampMs).Add(wallDeadline.Sub(wallAnchor))
	stampDue := !now.Before(stampDeadline)
	delta := wallDeadline.Sub(stampDeadline)
	if delta < 0 {
		delta = -delta
	}
	if delta <= LegacyShadowEpsilon {
		return LegacyShadow{StampDue: stampDue}
	}
	later := wallDeadline
	if stampDeadline.After(later) {
		later = stampDeadline
	}
	if stampDue && now.Sub(later) > LegacyShadowEpsilon {
		return LegacyShadow{Cause: "both_due", StampDue: true, Mismatch: true}
	}
	if stampDeadline.After(wallDeadline) {
		return LegacyShadow{Cause: "stamp_late", StampDue: stampDue, Mismatch: true}
	}
	return LegacyShadow{Cause: "stamp_early", StampDue: stampDue, Mismatch: true}
}
