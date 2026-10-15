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

	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/chain"
)

const pathSendTx = "/admin/v1/tx/send"

var chainCodec = codec.NewProtoCodec(chainTypes())

// how long the offer stands is a chain parameter, so the ttl only decides how soon the daemon repeats it
func (c *Client) OptIn(ctx context.Context, node vo.NodeRef, _ time.Duration) error {
	return c.send(ctx, &types.MsgRefreshTrainingNodeOptIn{
		Creator:  string(c.cfg.Participant),
		NodeIds:  []string{string(node.NodeID)},
		Endpoint: string(c.cfg.Endpoint),
	})
}

// the request id is derived, not random, so a retry of a release that already landed is a no-op on chain
func (c *Client) Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	return c.send(ctx, &types.MsgAutokickTrainshardNode{
		Creator:      string(c.cfg.Participant),
		TrainshardId: uint64(shardID),
		Participant:  string(node.Participant),
		NodeId:       string(node.NodeID),
		Reason:       string(reason),
		RequestId:    fmt.Sprintf("release/%s/%s/%s/%s", shardID, node.Participant, node.NodeID, reason),
	})
}

// the dapi answers once the chain accepts the transaction, not once it runs: the outcome is read back from the chain
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
		Codespace string `json:"codespace"`
		Code      uint32 `json:"code"`
		RawLog    string `json:"raw_log"`
	}
	if err := c.call(ctx, http.MethodPost, pathSendTx, payload, &answer); err != nil {
		return err
	}
	if answer.Code != 0 {
		return chain.Refused(msg, answer.Codespace, answer.Code, answer.RawLog)
	}
	return nil
}

func chainTypes() codectypes.InterfaceRegistry {
	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	return registry
}
