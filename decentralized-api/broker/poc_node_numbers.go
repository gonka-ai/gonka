package broker

import (
	"context"
	"decentralized-api/apiconfig"
	"fmt"
	"slices"
)

// Freeze nonce numbering for a stage; changing the stride can duplicate nonces.
// Store original node IDs so callbacks and weight distribution survive restarts.
type pocNodeNumbers struct {
	Height int64               `json:"height"`
	Models map[string][]string `json:"models"`
}

func (b *Broker) loadPoCNodeNumbers(ctx context.Context) error {
	if b.pocNodeNumbers == nil && b.configManager != nil && b.configManager.SqlDb() != nil {
		var numbers pocNodeNumbers
		found, err := apiconfig.KVGetJSON(ctx, b.configManager.SqlDb().GetDb(), "poc_node_numbers", &numbers)
		if err != nil {
			return err
		}
		if found {
			b.pocNodeNumbers = &numbers
		}
	}
	return nil
}

func (b *Broker) assignPoCNodeNumber(ctx context.Context, nodeID string, params *pocParams, cmd *StartPoCNodeCommandV2) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadPoCNodeNumbers(ctx); err != nil {
		return err
	}
	// Keep the first observed stage on global numbering. Backends may already
	// be generating with the old stride when this hotfix is installed.
	if b.pocNodeNumbers == nil {
		numbers := &pocNodeNumbers{Height: params.startPoCBlockHeight}
		if b.configManager != nil && b.configManager.SqlDb() != nil {
			if err := apiconfig.KVSetJSON(ctx, b.configManager.SqlDb().GetDb(), "poc_node_numbers", numbers); err != nil {
				return err
			}
		}
		b.pocNodeNumbers = numbers
	}
	if b.pocNodeNumbers.Height == params.startPoCBlockHeight && b.pocNodeNumbers.Models == nil {
		return nil
	}
	if b.pocNodeNumbers.Height != params.startPoCBlockHeight {
		numbers := &pocNodeNumbers{Height: params.startPoCBlockHeight, Models: make(map[string][]string)}
		for id, node := range b.nodes {
			model, ok := b.resolvePoCModelForNode(&node.State, node.Node.Models, params)
			if ok {
				numbers.Models[model.ModelId] = append(numbers.Models[model.ModelId], id)
			}
		}
		for _, ids := range numbers.Models {
			slices.Sort(ids)
		}
		if b.configManager != nil && b.configManager.SqlDb() != nil {
			if err := apiconfig.KVSetJSON(ctx, b.configManager.SqlDb().GetDb(), "poc_node_numbers", numbers); err != nil {
				return err
			}
		}
		b.pocNodeNumbers = numbers
	}
	ids := b.pocNodeNumbers.Models[cmd.Model]
	index := slices.Index(ids, nodeID)
	if index < 0 {
		return fmt.Errorf("node %s has no PoC allocation for model %s at height %d", nodeID, cmd.Model, params.startPoCBlockHeight)
	}
	cmd.ModelNodeID = &index
	cmd.TotalNodes = len(ids)
	return nil
}

// PoCNodeID translates a model-local number back to the weight distribution ID.
func (b *Broker) PoCNodeID(ctx context.Context, height int64, model string, index int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.loadPoCNodeNumbers(ctx); err != nil {
		return "", err
	}
	if b.pocNodeNumbers == nil || b.pocNodeNumbers.Height != height {
		return "", fmt.Errorf("no PoC node allocation at height %d", height)
	}
	ids := b.pocNodeNumbers.Models[model]
	if index < 0 || index >= len(ids) {
		return "", fmt.Errorf("unknown PoC node number %d for model %s", index, model)
	}
	return ids[index], nil
}
