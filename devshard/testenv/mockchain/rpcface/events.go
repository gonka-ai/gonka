package rpcface

import (
	"fmt"
	"strconv"

	abci "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/types"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

func escrowCreatedEvent(e *inferencetypes.DevshardEscrow) abci.Event {
	return abci.Event{
		Type: "devshard_escrow_created",
		Attributes: []abci.EventAttribute{
			{Key: "escrow_id", Value: strconv.FormatUint(e.Id, 10)},
			{Key: "creator", Value: e.Creator},
			{Key: "amount", Value: strconv.FormatUint(e.Amount, 10)},
			{Key: "epoch_index", Value: strconv.FormatUint(e.EpochIndex, 10)},
		},
	}
}

func escrowSettledEvent(id uint64, settler string, totalPayout, fees, remainder uint64) abci.Event {
	return abci.Event{
		Type: "devshard_escrow_settled",
		Attributes: []abci.EventAttribute{
			{Key: "escrow_id", Value: strconv.FormatUint(id, 10)},
			{Key: "settler", Value: settler},
			{Key: "total_payout", Value: strconv.FormatUint(totalPayout, 10)},
			{Key: "fees", Value: strconv.FormatUint(fees, 10)},
			{Key: "remainder", Value: strconv.FormatUint(remainder, 10)},
		},
	}
}

func txResult(height int64, events ...abci.Event) cmttypes.EventDataTx {
	return cmttypes.EventDataTx{TxResult: abci.TxResult{
		Height: height,
		Index:  0,
		Tx:     []byte(fmt.Sprintf("mock-tx-%d", height)),
		Result: abci.ExecTxResult{Events: events},
	}}
}

func newBlockEvent(chainID string, height int64) cmttypes.EventDataNewBlock {
	block := cmttypes.MakeBlock(height, nil, nil, nil)
	block.Header.ChainID = chainID
	return cmttypes.EventDataNewBlock{
		Block:   block,
		BlockID: cmttypes.BlockID{Hash: block.Hash()},
	}
}
