// Package chain reads what the chain holds about a shard. Nothing here writes: this machine keeps
// no key, so transactions go out through the dAPI
package chain

import (
	"context"
	"fmt"
	"strconv"
	"time"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Address string
	Poll    time.Duration
}

type Client struct {
	conn  *grpc.ClientConn
	query types.QueryClient
	poll  time.Duration
}

// asking again every second is as often as a chain that makes a block every few has anything new
const asOftenAsBlocks = time.Second

func Dial(cfg Config) (*Client, error) {
	conn, err := grpc.NewClient(cfg.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("chain %q: %w", cfg.Address, err)
	}
	if cfg.Poll <= 0 {
		cfg.Poll = asOftenAsBlocks
	}
	return &Client{conn: conn, query: types.NewQueryClient(conn), poll: cfg.Poll}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Height(ctx context.Context) (vo.Height, error) {
	var stamp metadata.MD
	if _, err := c.query.Params(ctx, &types.QueryParamsRequest{}, grpc.Header(&stamp)); err != nil {
		return 0, err
	}
	return height(stamp)
}

func (c *Client) Shard(ctx context.Context, shardID vo.ShardID) (shard.Shard, bool, error) {
	answer, err := c.query.Trainshard(ctx, &types.QueryGetTrainshardRequest{TrainshardId: uint64(shardID)})
	if err != nil {
		return shard.Shard{}, false, err
	}
	if !answer.Found || answer.Trainshard == nil {
		return shard.Shard{}, false, nil
	}
	record, err := toShard(answer.Trainshard)
	if err != nil {
		return shard.Shard{}, false, err
	}
	return record, true, nil
}

func (c *Client) ActiveShards(ctx context.Context) ([]shard.Shard, error) {
	shards, _, err := c.activeShards(ctx)
	return shards, err
}

func (c *Client) Reservation(ctx context.Context, node vo.NodeRef) (vo.ShardID, bool, error) {
	reservation, found, err := c.Reserved(ctx, node)
	return reservation.Shard, found, err
}

// Reserved walks the open shards: the chain answers what a shard reserves, never what a node is
// reserved by
func (c *Client) Reserved(ctx context.Context, node vo.NodeRef) (run.Reservation, bool, error) {
	shards, at, err := c.activeShards(ctx)
	if err != nil {
		return run.Reservation{}, false, err
	}
	for _, record := range shards {
		if record.Reserves(node) {
			return run.Reservation{Shard: record.ID, BaseImage: record.BaseImage, Active: record.IsActive(at)}, true, nil
		}
	}
	return run.Reservation{}, false, nil
}

func (c *Client) Hardware(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error) {
	answer, err := c.query.HardwareNodes(ctx, &types.QueryHardwareNodesRequest{Participant: string(node.Participant)})
	if err != nil {
		return vo.GPUInventory{}, err
	}
	if answer.Nodes == nil {
		return vo.GPUInventory{}, nil
	}
	for _, declared := range answer.Nodes.HardwareNodes {
		if declared.LocalId == string(node.NodeID) {
			return inventory(declared), nil
		}
	}
	return vo.GPUInventory{}, nil
}

func (c *Client) Watch(ctx context.Context) (<-chan struct{}, error) {
	hints := make(chan struct{}, 1)
	go func() {
		defer close(hints)

		ticker := time.NewTicker(c.poll)
		defer ticker.Stop()

		var last vo.Height
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			at, err := c.Height(ctx)
			if err != nil || at == last {
				continue
			}
			last = at
			select {
			case hints <- struct{}{}:
			default:
			}
		}
	}()
	return hints, nil
}

func (c *Client) activeShards(ctx context.Context) ([]shard.Shard, vo.Height, error) {
	var stamp metadata.MD
	answer, err := c.query.ActiveTrainshards(ctx, &types.QueryActiveTrainshardsRequest{}, grpc.Header(&stamp))
	if err != nil {
		return nil, 0, err
	}
	at, err := height(stamp)
	if err != nil {
		return nil, 0, err
	}

	shards := make([]shard.Shard, 0, len(answer.Trainshards))
	for _, held := range answer.Trainshards {
		record, err := toShard(held)
		if err != nil {
			return nil, 0, err
		}
		shards = append(shards, record)
	}
	return shards, at, nil
}

// height reads the block the chain answered at, which it stamps on every answer
func height(stamp metadata.MD) (vo.Height, error) {
	stamped := stamp.Get(grpctypes.GRPCBlockHeightHeader)
	if len(stamped) == 0 {
		return 0, fmt.Errorf("the chain answered without a %s", grpctypes.GRPCBlockHeightHeader)
	}
	at, err := strconv.ParseInt(stamped[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", grpctypes.GRPCBlockHeightHeader, stamped[0], err)
	}
	return vo.Height(at), nil
}

func toShard(held *types.Trainshard) (shard.Shard, error) {
	creator, err := vo.ParseAddress(held.Creator)
	if err != nil {
		return shard.Shard{}, fmt.Errorf("shard %d creator: %w", held.TrainshardId, err)
	}
	image, err := vo.ParseImageDigest(held.BaseImage)
	if err != nil {
		return shard.Shard{}, fmt.Errorf("shard %d base image: %w", held.TrainshardId, err)
	}
	record := shard.Shard{
		ID:              vo.ShardID(held.TrainshardId),
		Creator:         creator,
		Status:          toStatus(held.Status),
		BaseImage:       image,
		ExpiresAtHeight: vo.Height(held.ExpiresAtHeight),
	}
	if held.RunKey != "" {
		if record.RunKey, err = vo.ParseAddress(held.RunKey); err != nil {
			return shard.Shard{}, fmt.Errorf("shard %d run key: %w", held.TrainshardId, err)
		}
	}

	for _, reserved := range held.Nodes {
		if reserved.Status != types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE {
			continue
		}
		ref, err := vo.ParseNodeRef(reserved.Participant, reserved.NodeId)
		if err != nil {
			return shard.Shard{}, fmt.Errorf("shard %d node: %w", held.TrainshardId, err)
		}
		record.Nodes = append(record.Nodes, shard.ReservedNode{Ref: ref, ModelID: reserved.ModelId})
	}
	return record, nil
}

func toStatus(status types.TrainshardStatus) shard.Status {
	switch status {
	case types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE:
		return shard.StatusActive
	case types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED:
		return shard.StatusSettled
	case types.TrainshardStatus_TRAINSHARD_STATUS_EXPIRED:
		return shard.StatusExpired
	default:
		return shard.StatusUnknown
	}
}

// inventory counts every card the node declared; the chain matches a shard by the profile these add
// up to, so a node with mixed cards is named after the first kind it listed
func inventory(declared *types.HardwareNode) vo.GPUInventory {
	held := vo.GPUInventory{}
	for _, part := range declared.Hardware {
		if held.Model == "" {
			held.Model = part.Type
		}
		held.Count += int(part.Count)
	}
	return held
}
