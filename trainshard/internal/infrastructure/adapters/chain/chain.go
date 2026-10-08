// Package chain reads what the chain holds about a shard and signs the coordinator's own
// transactions. A host keeps no key: its transactions go out through the dAPI
package chain

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/clock"
	"trainshard/internal/infrastructure/adapters/gpuprofile"
)

type Config struct {
	Address string
	Poll    time.Duration
	Timeout time.Duration
}

type Client struct {
	conn   *grpc.ClientConn
	query  types.QueryClient
	poll   time.Duration
	clock  ports.Clock
	log    *slog.Logger
	grants grants
}

const asOftenAsBlocks = time.Second

const callTimeout = 30 * time.Second

func Dial(cfg Config, log *slog.Logger) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = callTimeout
	}
	conn, err := grpc.NewClient(cfg.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(within(cfg.Timeout)))
	if err != nil {
		return nil, fmt.Errorf("chain %q: %w", cfg.Address, err)
	}
	if cfg.Poll <= 0 {
		cfg.Poll = asOftenAsBlocks
	}
	return &Client{
		conn:   conn,
		query:  types.NewQueryClient(conn),
		poll:   cfg.Poll,
		clock:  clock.System{},
		log:    log,
		grants: grants{answers: map[grantKey]grantAnswer{}},
	}, nil
}

func unreachable(err error) error {
	return shared.New("CHAIN_UNREACHABLE", shared.ErrUnavailable, err.Error())
}

func within(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) ChainID(ctx context.Context) (string, error) {
	info, err := cmtservice.NewServiceClient(c.conn).GetNodeInfo(ctx, &cmtservice.GetNodeInfoRequest{})
	if err != nil {
		return "", fmt.Errorf("asking the chain its id: %w", err)
	}
	if info.DefaultNodeInfo == nil || info.DefaultNodeInfo.Network == "" {
		return "", fmt.Errorf("the chain did not say its id")
	}
	return info.DefaultNodeInfo.Network, nil
}

func (c *Client) Height(ctx context.Context) (vo.Height, error) {
	var stamp metadata.MD
	if _, err := c.query.Params(ctx, &types.QueryParamsRequest{}, grpc.Header(&stamp)); err != nil {
		return 0, unreachable(err)
	}
	return height(stamp)
}

func (c *Client) Shard(ctx context.Context, shardID vo.ShardID) (shard.Shard, bool, error) {
	answer, err := c.query.Trainshard(ctx, &types.QueryGetTrainshardRequest{TrainshardId: uint64(shardID)})
	if err != nil {
		return shard.Shard{}, false, unreachable(err)
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
	shards, _, _, err := c.activeShards(ctx)
	return shards, err
}

func (c *Client) Reservation(ctx context.Context, node vo.NodeRef) (vo.ShardID, bool, error) {
	reservation, found, err := c.Reserved(ctx, node)
	return reservation.Shard, found, err
}

// Reserved walks the open shards: the chain answers what a shard reserves, never what a node is
// reserved by
func (c *Client) Reserved(ctx context.Context, node vo.NodeRef) (run.Reservation, bool, error) {
	shards, unreadable, at, err := c.activeShards(ctx)
	if err != nil {
		return run.Reservation{}, false, err
	}
	// a node named in a record that does not parse is not free: found false would hand it back to
	// inference while the chain still reserves it
	if err, named := unreadable[node]; named {
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
		return vo.GPUInventory{}, unreachable(err)
	}
	if answer.Nodes == nil {
		return vo.GPUInventory{}, nil
	}
	for _, declared := range answer.Nodes.HardwareNodes {
		if declared.LocalId == string(node.NodeID) {
			return gpuprofile.FromHardware(declared.Hardware), nil
		}
	}
	return vo.GPUInventory{}, nil
}

// assemblyWindow reads when an assemble that may land up to lifetime blocks after it is sent is
// taken. The next PoC is known ahead: one that starts before the transaction lands or expires would
// refuse it, so the assemble waits that PoC out too. A confirmation PoC is random and still can
func (c *Client) assemblyWindow(ctx context.Context, lifetime int64) (vo.Height, vo.Height, error) {
	info, err := c.query.EpochInfo(ctx, &types.QueryEpochInfoRequest{})
	if err != nil {
		return 0, 0, unreachable(err)
	}
	if info.Params.EpochParams == nil {
		return 0, 0, fmt.Errorf("the chain answered without epoch params")
	}
	params := *info.Params.EpochParams
	opens := types.TrainshardAssemblyOpensAt(info.BlockHeight, info.LatestEpoch, params, info.ActiveConfirmationPocEvent)
	epoch := types.NewEpochContext(info.LatestEpoch, params)
	if next := epoch.NextEpochContext(); opens+lifetime >= next.StartOfPoC() && opens < next.EndOfPoCValidation() {
		opens = next.EndOfPoCValidation()
	}
	return vo.Height(info.BlockHeight), vo.Height(opens), nil
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

// a record that does not parse is skipped rather than failing the call, or one bad shard would
// stop every node on the host from converging, cleanup included
func (c *Client) activeShards(ctx context.Context) ([]shard.Shard, map[vo.NodeRef]error, vo.Height, error) {
	// the height comes from this same answer, so a shard is judged active at the height it was read at
	var stamp metadata.MD
	answer, err := c.query.ActiveTrainshards(ctx, &types.QueryActiveTrainshardsRequest{}, grpc.Header(&stamp))
	if err != nil {
		return nil, nil, 0, unreachable(err)
	}
	at, err := height(stamp)
	if err != nil {
		return nil, nil, 0, err
	}

	shards := make([]shard.Shard, 0, len(answer.Trainshards))
	unreadable := map[vo.NodeRef]error{}
	for _, held := range answer.Trainshards {
		record, err := toShard(held)
		if err != nil {
			c.log.Warn("skipping a shard record that does not parse", "shard_id", held.TrainshardId, "reason", err)
			skipped := shared.New("SHARD_UNREADABLE", shared.ErrUnavailable,
				fmt.Sprintf("shard %d reserves this node and its record does not parse: %v", held.TrainshardId, err))
			for _, node := range activeNodes(held) {
				unreadable[node] = skipped
			}
			continue
		}
		shards = append(shards, record)
	}
	return shards, unreadable, at, nil
}

// the names are taken as they stand: parsing them again could fail on the very entry that broke
// the record
func activeNodes(held *types.Trainshard) []vo.NodeRef {
	nodes := make([]vo.NodeRef, 0, len(held.Nodes))
	for _, reserved := range held.Nodes {
		if reserved.Status != types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE {
			continue
		}
		nodes = append(nodes, vo.NodeRef{
			Participant: vo.Participant(strings.TrimSpace(reserved.Participant)),
			NodeID:      vo.NodeID(strings.TrimSpace(reserved.NodeId)),
		})
	}
	return nodes
}

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
		endpoint, err := vo.ParseEndpoint(reserved.Endpoint)
		if err != nil {
			return shard.Shard{}, fmt.Errorf("shard %d node %s endpoint: %w", held.TrainshardId, reserved.NodeId, err)
		}
		record.Nodes = append(record.Nodes, shard.ReservedNode{Ref: ref, ModelID: reserved.ModelId, Endpoint: endpoint})
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
