package user

import (
	"devshard/chainoracle/blocks/headertime"
)

func recordTimeoutClockMismatch(side, kind, escrow string, nonce uint64, eval headertime.DueEval, wallUnix, stampMs int64, height uint64) {
	headertime.RecordClockMismatch(side, kind, escrow, nonce, eval, wallUnix, stampMs, height)
}
