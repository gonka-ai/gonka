package bridge

import "errors"

var (
	ErrNotImplemented      = errors.New("not implemented")
	ErrEscrowNotFound      = errors.New("escrow not found")
	ErrParticipantNotFound = errors.New("participant not found")
)
