package hosts

import (
	"context"
	"net/http"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

const meshDeadline = time.Minute

func (c *Client) Identities(ctx context.Context, shardID vo.ShardID, participant vo.Participant) ([]mesh.Identity, error) {
	var result contract.MeshResult
	path := toPath(contract.PathMesh, shardID, "")
	if err := c.call(ctx, participant, http.MethodGet, path, vo.NewRequestID(), nil, &result); err != nil {
		return nil, err
	}
	return toIdentities(participant, result.Items)
}

func (c *Client) Apply(ctx context.Context, config mesh.Config, node vo.NodeRef) error {
	id := vo.NewRequestID()
	body := fromConfig(config, node, id, c.clock.Now().Add(meshDeadline))

	var result contract.NodesResult
	path := toPath(contract.PathMesh, config.Shard, "")
	if err := c.call(ctx, node.Participant, http.MethodPost, path, id, body, &result); err != nil {
		return err
	}
	return toNodeError(result.Items)
}

func (c *Client) Probe(ctx context.Context, config mesh.Config, node vo.NodeRef) ([]mesh.Pair, error) {
	var result contract.ProbeResult
	path := toPath(contract.PathProbe, config.Shard, node.NodeID)
	if err := c.call(ctx, node.Participant, http.MethodPost, path, vo.NewRequestID(), nil, &result); err != nil {
		return nil, err
	}
	return toPairs(node, result), nil
}
