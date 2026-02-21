package types

// Slash reasons used by x/inference when calling x/collateral
const (
	SlashReasonInvalidation       = "invalidation"
	SlashReasonDowntime           = "downtime"
	SlashReasonForgedVotingResult = "forged_voting_result"
)
