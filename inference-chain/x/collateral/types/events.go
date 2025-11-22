package types

// Event types
const (
	EventTypeDepositCollateral  = "deposit_collateral"
	EventTypeWithdrawCollateral = "withdraw_collateral"
	EventTypeSlashCollateral    = "slash_collateral"
	EventTypeProcessWithdrawal  = "process_withdrawal"
)

// Event attribute keys
const (
	AttributeKeyParticipant     = "participant"
	AttributeKeyAmount          = "amount"
	AttributeKeyCompletionEpoch = "completion_epoch"
	AttributeKeySlashAmount     = "slash_amount"
	AttributeKeySlashFraction   = "slash_fraction"
	AttributeKeySlashReason     = "slash_reason"
)
