package chain

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type downStub struct {
	types.QueryClient
	err error
}

func (d downStub) Params(context.Context, *types.QueryParamsRequest, ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	return nil, d.err
}

func (d downStub) Trainshard(context.Context, *types.QueryGetTrainshardRequest, ...grpc.CallOption) (*types.QueryGetTrainshardResponse, error) {
	return nil, d.err
}

func (d downStub) ActiveTrainshards(context.Context, *types.QueryActiveTrainshardsRequest, ...grpc.CallOption) (*types.QueryActiveTrainshardsResponse, error) {
	return nil, d.err
}

func (d downStub) HardwareNodes(context.Context, *types.QueryHardwareNodesRequest, ...grpc.CallOption) (*types.QueryHardwareNodesResponse, error) {
	return nil, d.err
}

type activeStub struct {
	types.QueryClient
	shards []*types.Trainshard
}

func (a activeStub) ActiveTrainshards(_ context.Context, _ *types.QueryActiveTrainshardsRequest, opts ...grpc.CallOption) (*types.QueryActiveTrainshardsResponse, error) {
	for _, opt := range opts {
		if header, ok := opt.(grpc.HeaderCallOption); ok {
			*header.HeaderAddr = metadata.Pairs(grpctypes.GRPCBlockHeightHeader, "100")
		}
	}
	return &types.QueryActiveTrainshardsResponse{Trainshards: a.shards}, nil
}

func record(id uint64, baseImage string, nodes ...*types.TrainshardReservedNode) *types.Trainshard {
	return &types.Trainshard{
		TrainshardId:    id,
		Creator:         "gonka1creator",
		Status:          types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		BaseImage:       baseImage,
		ExpiresAtHeight: 1000,
		Nodes:           nodes,
	}
}

func reserving(nodeID string, status types.TrainshardNodeStatus) *types.TrainshardReservedNode {
	return &types.TrainshardReservedNode{Participant: "gonka1host", NodeId: nodeID, Endpoint: "https://gpu.example", Status: status}
}

func TestOneUnparsableShardRecordOnlyBlocksTheNodesItNames(t *testing.T) {
	const (
		active     = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
		autokicked = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_AUTOKICKED
	)
	digest := "ghcr.io/gonka/train@sha256:" + strings.Repeat("a", 64)
	shards := []*types.Trainshard{
		record(7, digest, reserving("node-1", active)),
		record(8, "not-a-digest", reserving("node-2", active), reserving("node-4", autokicked)),
		record(9, digest, reserving("node-5", active), reserving("../node-6", active)),
	}
	cases := []struct {
		name       string
		node       vo.NodeID
		found      bool
		shard      vo.ShardID
		unreadable bool
	}{
		{name: "a node in a record that parses", node: "node-1", found: true, shard: 7},
		{name: "a node in a record with a bad base image", node: "node-2", unreadable: true},
		{name: "a node another node's bad entry broke the record of", node: "node-5", unreadable: true},
		{name: "a node the unparsable record already autokicked", node: "node-4"},
		{name: "a node no shard names", node: "node-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			client := &Client{query: activeStub{shards: shards}, log: slog.New(slog.DiscardHandler)}

			// act
			reservation, found, err := client.Reserved(context.Background(), vo.NodeRef{Participant: "gonka1host", NodeID: tc.node})

			// assert
			if tc.unreadable {
				if !errors.Is(err, shared.ErrUnavailable) || shared.CodeOf(err) != "SHARD_UNREADABLE" {
					t.Fatalf("got %v (%s), want SHARD_UNREADABLE so the node is not taken for free", err, shared.CodeOf(err))
				}
				return
			}
			if err != nil {
				t.Fatalf("got %v, want the bad record kept from failing a node it does not name", err)
			}
			if found != tc.found || reservation.Shard != tc.shard {
				t.Fatalf("got shard %d found %v, want shard %d found %v", reservation.Shard, found, tc.shard, tc.found)
			}
		})
	}
}

func TestActiveShardsLeavesOutARecordThatDoesNotParse(t *testing.T) {
	// arrange
	digest := "ghcr.io/gonka/train@sha256:" + strings.Repeat("a", 64)
	client := &Client{
		query: activeStub{shards: []*types.Trainshard{
			record(7, digest),
			record(8, "not-a-digest"),
		}},
		log: slog.New(slog.DiscardHandler),
	}

	// act
	shards, err := client.ActiveShards(context.Background())

	// assert
	if err != nil {
		t.Fatalf("active shards: %v", err)
	}
	if len(shards) != 1 || shards[0].ID != 7 {
		t.Fatalf("got %+v, want only the shard that parses", shards)
	}
}

func TestAChainThatCannotBeReadIsUnavailable(t *testing.T) {
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node-1"}
	cases := []struct {
		name string
		read func(*Client) error
	}{
		{"height", func(c *Client) error { _, err := c.Height(context.Background()); return err }},
		{"shard", func(c *Client) error { _, _, err := c.Shard(context.Background(), 7); return err }},
		{"active shards", func(c *Client) error { _, err := c.ActiveShards(context.Background()); return err }},
		{"reservation", func(c *Client) error { _, _, err := c.Reserved(context.Background(), node); return err }},
		{"hardware", func(c *Client) error { _, err := c.Hardware(context.Background(), node); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			client := &Client{query: downStub{err: status.Error(codes.Unavailable, "connection refused")}}

			// act
			err := tc.read(client)

			// assert
			if !errors.Is(err, shared.ErrUnavailable) || shared.CodeOf(err) != "CHAIN_UNREACHABLE" {
				t.Fatalf("got %v (%s), want CHAIN_UNREACHABLE as unavailable rather than an internal error", err, shared.CodeOf(err))
			}
		})
	}
}
