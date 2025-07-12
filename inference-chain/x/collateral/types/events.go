package types

// Event types
const (
	EventTypeDepositCollateral  = "deposit_collateral"
	EventTypeWithdrawCollateral = "withdraw_collateral"
	EventTypeSlashCollateral    = "slash_collateral"

	AttributeKeyParticipant     = "participant"
	AttributeKeyAmount          = "amount"
	AttributeKeyCompletionEpoch = "completion_epoch"
	AttributeKeyAmountSlashed   = "amount_slashed"
)
