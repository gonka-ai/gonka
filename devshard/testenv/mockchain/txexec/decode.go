package txexec

import (
	"fmt"

	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/gogoproto/proto"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const (
	CreateEscrowMsgTypeURL = "/inference.inference.MsgCreateDevshardEscrow"
	SettleEscrowMsgTypeURL = "/inference.inference.MsgSettleDevshardEscrow"
)

// DecodedMsg is one inference message extracted from a signed tx.
type DecodedMsg struct {
	Create *inferencetypes.MsgCreateDevshardEscrow
	Settle *inferencetypes.MsgSettleDevshardEscrow
}

// DecodeTxMessages parses a signed TxRaw and returns supported inference messages.
func DecodeTxMessages(txBytes []byte) ([]DecodedMsg, error) {
	var raw txtypes.TxRaw
	if err := proto.Unmarshal(txBytes, &raw); err != nil {
		return nil, fmt.Errorf("decode tx raw: %w", err)
	}
	var body txtypes.TxBody
	if err := proto.Unmarshal(raw.BodyBytes, &body); err != nil {
		return nil, fmt.Errorf("decode tx body: %w", err)
	}
	if len(body.Messages) == 0 {
		return nil, fmt.Errorf("tx has no messages")
	}
	out := make([]DecodedMsg, 0, len(body.Messages))
	for _, anyMsg := range body.Messages {
		if anyMsg == nil {
			continue
		}
		switch anyMsg.TypeUrl {
		case CreateEscrowMsgTypeURL:
			var msg inferencetypes.MsgCreateDevshardEscrow
			if err := proto.Unmarshal(anyMsg.Value, &msg); err != nil {
				return nil, fmt.Errorf("decode MsgCreateDevshardEscrow: %w", err)
			}
			out = append(out, DecodedMsg{Create: &msg})
		case SettleEscrowMsgTypeURL:
			var msg inferencetypes.MsgSettleDevshardEscrow
			if err := proto.Unmarshal(anyMsg.Value, &msg); err != nil {
				return nil, fmt.Errorf("decode MsgSettleDevshardEscrow: %w", err)
			}
			out = append(out, DecodedMsg{Settle: &msg})
		default:
			return nil, fmt.Errorf("unsupported message type %q", anyMsg.TypeUrl)
		}
	}
	return out, nil
}
