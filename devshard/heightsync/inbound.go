package heightsync

import (
	"strings"
	"time"

	"devshard/chainoracle/blocks"
)

// DefaultOriginatorFreshness is the courier carry-forward budget F (proposal default).
const DefaultOriginatorFreshness = 60 * time.Second

// AnchorCadenceTag classifies how an inbound user Anchor relates to sync-turn cadence.
type AnchorCadenceTag string

const (
	TagCadence AnchorCadenceTag = "cadence"
	TagLazy    AnchorCadenceTag = "lazy"
)

// InboundAnchorResult is the receiver-side validation outcome for a user request Anchor.
type InboundAnchorResult string

const (
	ResultOmit                InboundAnchorResult = "omit"
	ResultValidAnchor         InboundAnchorResult = "valid_anchor"
	ResultValidLazyAnchor     InboundAnchorResult = "valid_lazy_anchor"
	ResultInvalidStaleOrigin  InboundAnchorResult = "invalid_stale_origin"
	ResultInvalidSyncTurnOmit InboundAnchorResult = "invalid_sync_turn_omit"
)

// InboundValidateParams drives ClassifyInboundRequestAnchor.
type InboundValidateParams struct {
	Nonce     uint64
	K         uint64
	Slots     uint64
	Escrow    *EscrowHeightSyncHints
	Now       time.Time
	Freshness time.Duration
	OracleHdr *blocks.Header
}

// InboundValidation is the outcome of receiver-side Anchor classification.
type InboundValidation struct {
	Result InboundAnchorResult
	Tag    AnchorCadenceTag
	Reason string
	Trust  AttestationTrust
}

// NonceInSyncTurn reports whether nonce falls in initial, periodic, or forced sync-turn
// windows (same cadence as AnchorScheduler.shouldEmit, excluding SessionStart/ForceAnchor hints).
func NonceInSyncTurn(nonce, k, slots uint64, escrow *EscrowHeightSyncHints) bool {
	if inEscrowForcedWindow(DecideHints{Nonce: nonce, Escrow: escrow}) {
		return true
	}
	if nonce == 0 {
		return false
	}
	if slots == 0 {
		slots = defaultSlotsNum
	}
	if k == 0 {
		k = defaultAnchorK
	}
	if nonce <= slots {
		return true
	}
	if nonce >= k && nonce%k < slots {
		if escrow != nil && escrow.CadenceSwallowUntil != 0 {
			if nonce > escrow.SwallowFe && nonce <= escrow.CadenceSwallowUntil {
				return false
			}
		}
		return true
	}
	return false
}

func isCarryForwardAnchor(hs *HeightSyncSection) bool {
	if hs == nil {
		return false
	}
	return strings.TrimSpace(hs.OriginatorSenderID) != "" || hs.OriginatorTimestampMs > 0
}

func originatorObservedAt(hs *HeightSyncSection) int64 {
	if hs == nil {
		return 0
	}
	if hs.OriginatorTimestampMs > 0 {
		return hs.OriginatorTimestampMs
	}
	return hs.TimestampUnixMs
}

func freshnessOK(hs *HeightSyncSection, now time.Time, freshness time.Duration) bool {
	if freshness <= 0 {
		freshness = DefaultOriginatorFreshness
	}
	ts := originatorObservedAt(hs)
	if ts <= 0 {
		// A carry-forward with no originator time is arbitrarily old: the
		// carrier can replay a cached (height, hash) forever. First-party
		// anchors never reach this function (ClassifyInboundRequestAnchor
		// gates it on isCarryForwardAnchor).
		return false
	}
	return now.Sub(time.UnixMilli(ts)) <= freshness
}

// ClassifyInboundRequestAnchor applies sync-turn vs lazy classification and the
// originator freshness gate for courier carry-forward Anchors.
func ClassifyInboundRequestAnchor(hs *HeightSyncSection, p InboundValidateParams) InboundValidation {
	if !IsAnchorSection(hs) {
		if NonceInSyncTurn(p.Nonce, p.K, p.Slots, p.Escrow) {
			return InboundValidation{Result: ResultInvalidSyncTurnOmit, Reason: "sync_turn_anchor_missing"}
		}
		return InboundValidation{Result: ResultOmit}
	}

	if isCarryForwardAnchor(hs) {
		if !freshnessOK(hs, p.Now, p.Freshness) {
			return InboundValidation{
				Result: ResultInvalidStaleOrigin,
				Reason: "stale_origin",
				Trust:  TrustDisputeCarrier,
			}
		}
	}

	trust := InboundTrust(hs, p.OracleHdr)
	if NonceInSyncTurn(p.Nonce, p.K, p.Slots, p.Escrow) {
		return InboundValidation{
			Result: ResultValidAnchor,
			Tag:    TagCadence,
			Trust:  trust,
		}
	}
	if isCarryForwardAnchor(hs) {
		return InboundValidation{
			Result: ResultValidLazyAnchor,
			Tag:    TagLazy,
			Trust:  trust,
		}
	}
	// Non-carry-forward Anchor outside sync turn (host-style self attestation).
	return InboundValidation{
		Result: ResultValidAnchor,
		Tag:    TagLazy,
		Trust:  trust,
	}
}
