package types

// DevshardValidationRateForCreate returns the validation_rate snapshotted onto a
// DevshardEscrow at create. Governance zero falls back to the compiled default.
func DevshardValidationRateForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil || ep.ValidationRate == 0 {
		return DefaultDevshardValidationRate
	}
	return ep.ValidationRate
}

// DevshardChatFrameJoinForCreate returns the chat_frame_join snapshotted onto a
// DevshardEscrow at create. Governance zero falls back to the compiled default.
func DevshardChatFrameJoinForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil || ep.ChatFrameJoin == 0 {
		return DefaultDevshardChatFrameJoin
	}
	return ep.ChatFrameJoin
}

// DevshardChatFrameSignEveryForCreate returns the chat_frame_sign_every
// snapshotted onto a DevshardEscrow at create. Governance zero falls back to
// the compiled default.
func DevshardChatFrameSignEveryForCreate(ep *DevshardEscrowParams) uint32 {
	if ep == nil || ep.ChatFrameSignEvery == 0 {
		return DefaultDevshardChatFrameSignEvery
	}
	return ep.ChatFrameSignEvery
}
