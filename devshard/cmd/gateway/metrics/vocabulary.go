package metrics

import (
	"maps"
	"slices"

	"devshard/heightsync"
	"devshard/types"
)

// What a height-sync exchange carried, and how many bits a peer-seen bitmap byte holds.
const (
	exchangeTotal       = "total"
	exchangeWithSection = "section"
	exchangeWithStamp   = "stamp"
	exchangeAgreed      = "agree"

	bitsPerByte = 8
)

// What one sweep tick did with the nonces it found.
const (
	sweepOutcomeApplied = "applied"
	sweepOutcomeFailed  = "failed"
)

// Why a store writer lost a row: shed on a full queue or a closed writer, or refused by the database.
const (
	lostCauseShed        = "shed"
	lostCauseWriteFailed = "write_failed"
)

// Why an active escrow's session would not open; any other cause is UnopenedOther. See operations.md, "Boot".
const (
	UnopenedLocalState = "local_state_unrecoverable"
	UnopenedKeyMissing = "private_key_missing"
	UnopenedChain      = "chain_unavailable"
	UnopenedDraining   = "draining"
	UnopenedOther      = "other"
)

func unopenedReasonLabel(reason string) string {
	switch reason {
	case UnopenedLocalState, UnopenedKeyMissing, UnopenedChain, UnopenedDraining:
		return reason
	}
	return UnopenedOther
}

// What a probe wave did.
const (
	HostPingTickStarted = "started"
	HostPingTickSkipped = "skipped"
)

// What a host's RPC stats report is folded to, and the labels they are exported under. See ../hostrpc/README.md.
const (
	hostRPCOther          = "other"
	hostRPCReconnectWatch = "watch"
	hostRPCReconnectTTL   = "ttl"
	hostRPCAttachFloor    = "floor"
	hostRPCAttachOrigin   = "origin"
	hostRPCParticipant    = "participant_key"
	hostRPCEndpoint       = "endpoint"
	hostRPCZone           = "zone"
	hostRPCReason         = "reason"
)

// See README.md, "Race labels".
const (
	outcomeFailure = "failure"

	statusNoCode = "0"

	reasonNone             = "none"
	reasonNoAttempts       = "no_attempts"
	reasonEscrowMissing    = "escrow_missing"
	reasonBalanceExhausted = "balance_exhausted"
	reasonReplyRefused     = "reply_refused"
)

// The host sync states and anchor kinds one escrow's height-sync series are labelled with.
var (
	syncStates  = slices.Sorted(maps.Values(types.SyncState_name))
	anchorKinds = []string{heightsync.AnchorKindCadence, heightsync.AnchorKindHeartbeat, heightsync.AnchorKindForced, heightsync.AnchorKindResponse}
)
