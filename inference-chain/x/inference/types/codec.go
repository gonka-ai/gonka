package types

import (
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
	// this line is used by starport scaffolding # 1
)

func RegisterInterfaces(registry cdctypes.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgStartInference{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgFinishInference{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitNewParticipant{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgValidation{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitNewUnfundedParticipant{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgInvalidateInference{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgRevalidateInference{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgClaimRewards{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitPocBatch{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitPocValidation{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitSeed{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitUnitOfComputePriceProposal{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgRegisterModel{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgCreateTrainingTask{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitHardwareDiff{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgClaimTrainingTaskForAssignment{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgAssignTrainingTask{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgCreatePartialUpgrade{},
	)
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgSubmitTrainingKvRecord{},
	)
	// this line is used by starport scaffolding # 3

	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
