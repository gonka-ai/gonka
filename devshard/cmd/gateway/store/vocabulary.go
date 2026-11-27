package store

import "time"

// Role is what an escrow is for in the rotation, as a row stores it.
type Role string

// RequestOutcome is how a request ended, as the ledger stores it.
type RequestOutcome string

const (
	RoleUnset   Role = ""
	RoleTemp    Role = "temp"
	RoleRegular Role = "regular"
	RoleReserve Role = "reserve"

	RequestSettled  RequestOutcome = "settled"
	RequestFailed   RequestOutcome = "failed"
	RequestNoEscrow RequestOutcome = "no_escrow"

	storedTimeLayout  = "2006-01-02T15:04:05.000000000Z07:00"
	settleStampLayout = time.DateTime

	ledgerQueueDepth       = 1024
	retentionSweepEvery    = time.Minute
	owedVoteQueueDepth     = 1024
	maxOwedVotePromptBytes = 1 << 20
)
