package fake

import (
	"encoding/json"
	"fmt"
	"os"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type seed struct {
	Height   int64          `json:"height"`
	Shards   []seedShard    `json:"shards"`
	Hardware []seedHardware `json:"hardware"`
}

type seedShard struct {
	ID              uint64     `json:"id"`
	Creator         string     `json:"creator"`
	RunKey          string     `json:"run_key"`
	Status          string     `json:"status"`
	BaseImage       string     `json:"base_image_digest"`
	ExpiresAtHeight int64      `json:"expires_at_height"`
	Nodes           []seedNode `json:"nodes"`
}

type seedNode struct {
	Participant string `json:"participant"`
	NodeID      string `json:"node_id"`
	ModelID     string `json:"model_id"`
}

type seedHardware struct {
	Participant string `json:"participant"`
	NodeID      string `json:"node_id"`
	Model       string `json:"model"`
	Count       int    `json:"count"`
}

func Load(path string, clock ports.Clock) (*Chain, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("chain seed: %w", err)
	}

	var file seed
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("chain seed %q: %w", path, err)
	}

	chain := New(clock)
	chain.height = vo.Height(file.Height)

	for _, entry := range file.Hardware {
		node, err := vo.ParseNodeRef(entry.Participant, entry.NodeID)
		if err != nil {
			return nil, err
		}
		chain.hardware[node] = vo.GPUInventory{Model: entry.Model, Count: entry.Count}
	}

	for _, entry := range file.Shards {
		record := shard.Shard{
			ID:              vo.ShardID(entry.ID),
			Creator:         vo.Address(entry.Creator),
			RunKey:          vo.Address(entry.RunKey),
			Status:          shard.Status(entry.Status),
			BaseImage:       vo.ImageDigest(entry.BaseImage),
			ExpiresAtHeight: vo.Height(entry.ExpiresAtHeight),
		}
		for _, member := range entry.Nodes {
			node, err := vo.ParseNodeRef(member.Participant, member.NodeID)
			if err != nil {
				return nil, err
			}
			record.Nodes = append(record.Nodes, shard.ReservedNode{Ref: node, ModelID: member.ModelID})
			chain.reservations[node] = record.ID
		}
		chain.shards[record.ID] = record
	}
	return chain, nil
}
