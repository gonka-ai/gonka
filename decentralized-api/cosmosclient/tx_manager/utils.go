package tx_manager

import (
	"decentralized-api/logging"
	"fmt"
	"sync/atomic"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/golang/protobuf/proto"
	"github.com/productscience/inference/x/inference/types"
)

func ParseMsgResponse(data []byte, msgIndex int, dstMsg proto.Message) error {
	var txMsgData sdk.TxMsgData
	if err := proto.Unmarshal(data, &txMsgData); err != nil {
		logging.Error("Failed to unmarshal TxMsgData", types.Messages, "error", err, "data", data)
		return fmt.Errorf("failed to unmarshal TxMsgData: %w", err)
	}

	logging.Info("Found messages", types.Messages, "len(messages)", len(txMsgData.MsgResponses), "messages", txMsgData.MsgResponses)
	if msgIndex < 0 || msgIndex >= len(txMsgData.MsgResponses) {
		logging.Error("Message index out of range", types.Messages, "msgIndex", msgIndex, "len(messages)", len(txMsgData.MsgResponses))
		return fmt.Errorf(
			"message index %d out of range: got %d responses",
			msgIndex, len(txMsgData.MsgResponses),
		)
	}

	anyResp := txMsgData.MsgResponses[msgIndex]
	if err := proto.Unmarshal(anyResp.Value, dstMsg); err != nil {
		logging.Error("Failed to unmarshal response", types.Messages, "error", err, "msgIndex", msgIndex, "response", anyResp.Value)
		return fmt.Errorf("failed to unmarshal response at index %d: %w", msgIndex, err)
	}
	return nil
}

var lastNanos atomic.Int64

func nowNanoUnique() int64 {
	for {
		now := time.Now().UnixNano()
		prev := lastNanos.Load()
		next := now
		if next <= prev {
			next = prev + 1
		}
		if lastNanos.CompareAndSwap(prev, next) {
			return next
		}
	}
}

func getTimestamp(duration time.Duration) time.Time {
	nanos := nowNanoUnique()
	return time.Unix(0, nanos).Add(duration)
}
