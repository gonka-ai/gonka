package dapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

const pathSendTx = "/admin/v1/tx/send"

var chainCodec = codec.NewProtoCodec(chainTypes())

// OptIn offers the node for training. How long the offer stands is a chain parameter, not ours, so
// the ttl we were asked for only decides how soon we say it again
func (c *Client) OptIn(ctx context.Context, node vo.NodeRef, _ time.Duration) error {
	return c.send(ctx, &types.MsgRefreshTrainingNodeOptIn{
		Creator: string(c.cfg.Participant),
		NodeIds: []string{string(node.NodeID)},
	})
}

// Release hands the reservation back. The chain calls this an autokick whoever asks for it, and a
// host asking for its own node is allowed to. The id is the same on every retry so a repeat of a
// release that already landed changes nothing
func (c *Client) Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	return c.send(ctx, &types.MsgAutokickTrainshardNode{
		Creator:      string(c.cfg.Participant),
		TrainshardId: uint64(shardID),
		Participant:  string(node.Participant),
		NodeId:       string(node.NodeID),
		Reason:       string(reason),
		RequestId:    fmt.Sprintf("release/%s/%s/%s", shardID, node.NodeID, reason),
	})
}

// send hands the message to the dapi, which signs it as this participant and broadcasts it. The
// answer only says the chain accepted the transaction; whether it did what we asked is read back
// from the chain like everything else
func (c *Client) send(ctx context.Context, msg sdk.Msg) error {
	message, err := codectypes.NewAnyWithValue(msg)
	if err != nil {
		return err
	}
	payload, err := chainCodec.MarshalJSON(&txtypes.Tx{Body: &txtypes.TxBody{Messages: []*codectypes.Any{message}}})
	if err != nil {
		return err
	}

	var answer struct {
		Code   uint32 `json:"code"`
		RawLog string `json:"raw_log"`
	}
	if err := c.call(ctx, http.MethodPost, pathSendTx, payload, &answer); err != nil {
		return err
	}
	if answer.Code != 0 {
		return shared.New("CHAIN_REFUSED", shared.ErrUnavailable,
			fmt.Sprintf("the chain refused %s with code %d: %s", sdk.MsgTypeURL(msg), answer.Code, answer.RawLog))
	}
	return nil
}

func chainTypes() codectypes.InterfaceRegistry {
	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	return registry
}
