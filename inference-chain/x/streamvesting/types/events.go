package types

// Event types for streamvesting module
const (
	EventTypeVestReward          = "vest_reward"
	EventTypeUnlockTokens        = "unlock_tokens"
	EventTypeTransferWithVesting = "transfer_with_vesting"
)

// Event attributes
const (
	AttributeKeyParticipant    = "participant"
	AttributeKeyAmount         = "amount"
	AttributeKeyVestingEpochs  = "vesting_epochs"
	AttributeKeyUnlockedAmount = "unlocked_amount"
	AttributeKeyEpoch          = "epoch"
	AttributeKeySender         = "sender"
	AttributeKeyRecipient      = "recipient"
)
