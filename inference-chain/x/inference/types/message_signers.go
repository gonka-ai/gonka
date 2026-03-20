package types

// This file defines GetSignersStrings() implementations for messages to satisfy the
// HasSigners interface expected by keeper.CheckPermission.

// Governance-authority messages
func (msg *MsgUpdateParams) GetSignersStrings() []string  { return []string{msg.Authority} }
func (msg *MsgRegisterModel) GetSignersStrings() []string { return []string{msg.Authority} }
func (msg *MsgDeleteGovernanceModel) GetSignersStrings() []string {
	return []string{msg.Authority}
}
func (msg *MsgApproveBridgeTokenForTrading) GetSignersStrings() []string {
	return []string{msg.Authority}
}
func (msg *MsgRegisterBridgeAddresses) GetSignersStrings() []string   { return []string{msg.Authority} }
func (msg *MsgRegisterLiquidityPool) GetSignersStrings() []string     { return []string{msg.Authority} }
func (msg *MsgRegisterTokenMetadata) GetSignersStrings() []string     { return []string{msg.Authority} }
func (msg *MsgApproveIbcTokenForTrading) GetSignersStrings() []string { return []string{msg.Authority} }
func (msg *MsgRegisterIbcTokenMetadata) GetSignersStrings() []string  { return []string{msg.Authority} }
func (msg *MsgMigrateAllWrappedTokens) GetSignersStrings() []string   { return []string{msg.Authority} }
func (msg *MsgRegisterWrappedTokenContract) GetSignersStrings() []string {
	return []string{msg.Authority}
}
func (msg *MsgAddUserToTrainingAllowList) GetSignersStrings() []string {
	return []string{msg.Authority}
}
func (msg *MsgCreatePartialUpgrade) GetSignersStrings() []string { return []string{msg.Authority} }
func (msg *MsgRemoveUserFromTrainingAllowList) GetSignersStrings() []string {
	return []string{msg.Authority}
}
func (msg *MsgSetTrainingAllowList) GetSignersStrings() []string { return []string{msg.Authority} }
func (msg *MsgAddParticipantsToAllowList) GetSignersStrings() []string {
	return []string{msg.Authority}
}
func (msg *MsgRemoveParticipantsFromAllowList) GetSignersStrings() []string {
	return []string{msg.Authority}
}

// Creator signed messages
func (msg *MsgCreateTrainingTask) GetSignersStrings() []string      { return []string{msg.Creator} }
func (msg *MsgCreateDummyTrainingTask) GetSignersStrings() []string { return []string{msg.Creator} }
func (msg *MsgAssignTrainingTask) GetSignersStrings() []string      { return []string{msg.Creator} }
func (msg *MsgClaimTrainingTaskForAssignment) GetSignersStrings() []string {
	return []string{msg.Creator}
}
func (msg *MsgFinishInference) GetSignersStrings() []string      { return []string{msg.Creator} }
func (msg *MsgInvalidateInference) GetSignersStrings() []string  { return []string{msg.Creator} }
func (msg *MsgRevalidateInference) GetSignersStrings() []string  { return []string{msg.Creator} }
func (msg *MsgStartInference) GetSignersStrings() []string       { return []string{msg.Creator} }
func (msg *MsgJoinTraining) GetSignersStrings() []string         { return []string{msg.Creator} }
func (msg *MsgJoinTrainingStatus) GetSignersStrings() []string   { return []string{msg.Creator} }
func (msg *MsgSetBarrier) GetSignersStrings() []string           { return []string{msg.Creator} }
func (msg *MsgSubmitHardwareDiff) GetSignersStrings() []string   { return []string{msg.Creator} }
func (msg *MsgSubmitNewParticipant) GetSignersStrings() []string { return []string{msg.Creator} }
func (msg *MsgSubmitNewUnfundedParticipant) GetSignersStrings() []string {
	return []string{msg.Creator}
}
func (msg *MsgSubmitPocBatch) GetSignersStrings() []string           { return []string{msg.Creator} }
func (msg *MsgSubmitPocValidation) GetSignersStrings() []string      { return []string{msg.Creator} }
func (msg *MsgSubmitPocValidationsV2) GetSignersStrings() []string   { return []string{msg.Creator} }
func (msg *MsgPoCV2StoreCommit) GetSignersStrings() []string         { return []string{msg.Creator} }
func (msg *MsgMLNodeWeightDistribution) GetSignersStrings() []string { return []string{msg.Creator} }
func (msg *MsgSubmitSeed) GetSignersStrings() []string               { return []string{msg.Creator} }
func (msg *MsgSubmitTrainingKvRecord) GetSignersStrings() []string   { return []string{msg.Creator} }
func (msg *MsgSubmitUnitOfComputePriceProposal) GetSignersStrings() []string {
	return []string{msg.Creator}
}
func (msg *MsgTrainingHeartbeat) GetSignersStrings() []string       { return []string{msg.Creator} }
func (msg *MsgValidation) GetSignersStrings() []string              { return []string{msg.Creator} }
func (msg *MsgClaimRewards) GetSignersStrings() []string            { return []string{msg.Creator} }
func (msg *MsgRequestBridgeMint) GetSignersStrings() []string       { return []string{msg.Creator} }
func (msg *MsgRequestBridgeWithdrawal) GetSignersStrings() []string { return []string{msg.Creator} }

// Subnet escrow messages
func (msg *MsgCreateSubnetEscrow) GetSignersStrings() []string { return []string{msg.Creator} }
func (msg *MsgSettleSubnetEscrow) GetSignersStrings() []string { return []string{msg.Settler} }

// And one validator signed message?
func (msg *MsgBridgeExchange) GetSignersStrings() []string { return []string{msg.Validator} }
