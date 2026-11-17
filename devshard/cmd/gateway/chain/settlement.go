package chain

import (
	"errors"

	"devshard/gatewayproto"
)

// ErrDevshardBusy means an escrow cannot be settled yet; registry raises it and escrow waits on it.
var ErrDevshardBusy = errors.New("devshard busy")

// SettlementInput is a settle payload with binary fields already decoded; it maps 1:1 onto MsgSettleDevshardEscrow.
type SettlementInput = gatewayproto.SettlementInput

type SettlementHostStat = gatewayproto.SettlementHostStat

// SettlementSlotSig is one slot's settlement signature, already decoded.
type SettlementSlotSig = gatewayproto.SettlementSlotSig
