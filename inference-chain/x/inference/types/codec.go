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
		&MsgSubmitPoC{},
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
		&MsgSubmitPocBatch{},
	)
	// this line is used by starport scaffolding # 3

	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
