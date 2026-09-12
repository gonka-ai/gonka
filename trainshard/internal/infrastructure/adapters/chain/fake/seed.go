package fake

import (
	"encoding/json"
	"fmt"
	"os"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/gpuprofile"
)

type seed struct {
	Height   int64          `json:"height"`
	Shards   []seedShard    `json:"shards"`
	Hardware []seedHardware `json:"hardware"`
	WarmKeys []seedWarmKey  `json:"warm_keys"`
}

type seedWarmKey struct {
	Participant string `json:"participant"`
	Address     string `json:"address"`
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

func Load(path string) (*Chain, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("chain seed: %w", err)
	}

	var file seed
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("chain seed %q: %w", path, err)
	}

	chain := newChain()
	chain.height = vo.Height(file.Height)

	for _, entry := range file.Hardware {
		node, err := vo.ParseNodeRef(entry.Participant, entry.NodeID)
		if err != nil {
			return nil, err
		}
		chain.hardware[node] = gpuprofile.Declared(entry.Model, entry.Count)
	}

	for _, entry := range file.WarmKeys {
		chain.warmKeys[vo.Address(entry.Address)] = vo.Participant(entry.Participant)
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
