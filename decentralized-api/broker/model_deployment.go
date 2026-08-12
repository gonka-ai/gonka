package broker

import (
	"common/logging"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"decentralized-api/apiconfig"

	"github.com/productscience/inference/x/inference/types"
)

type ModelDeployment struct {
	GovernanceID string
	LoadModel    string
	LoadCommit   string
	Args         []string
}

func (b *Broker) ResolveModelDeployment(model types.Model, local ModelArgs) ModelDeployment {
	args := b.MergeModelArgs(model.ModelArgs, local.Args)
	deployment := ModelDeployment{
		GovernanceID: model.Id,
		LoadModel:    model.Id,
		Args:         args,
	}
	if local.ModelOverride == nil {
		return deployment
	}

	deployment.LoadModel = local.ModelOverride.HfRepo
	deployment.LoadCommit = local.ModelOverride.HfCommit
	deployment.Args = removeDeploymentArgs(args)
	if deployment.LoadCommit != "" {
		deployment.Args = append(deployment.Args, "--revision", deployment.LoadCommit)
	}
	deployment.Args = append(deployment.Args, "--served-model-name", deployment.GovernanceID)
	return deployment
}

func (d ModelDeployment) Fingerprint() string {
	encoded, _ := json.Marshal(d)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func removeDeploymentArgs(args []string) []string {
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		key := strings.SplitN(args[i], "=", 2)[0]
		if !isDeploymentArg(key) {
			cleaned = append(cleaned, args[i])
			continue
		}
		if strings.Contains(args[i], "=") {
			continue
		}
		if key == "--served-model-name" {
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
		}
	}
	return cleaned
}

func isDeploymentArg(key string) bool {
	return key == "--model" || key == "--revision" || key == "--served-model-name"
}

func loadedModelsContain(loadedModels []string, expected string) bool {
	for _, loaded := range loadedModels {
		if loaded == expected {
			return true
		}
	}
	return false
}

func modelArgsFromConfig(config apiconfig.ModelConfig) ModelArgs {
	return ModelArgs{
		Args:          append([]string(nil), config.Args...),
		ModelOverride: copyModelOverride(config.ModelOverride),
	}
}

func modelConfigFromArgs(config ModelArgs) apiconfig.ModelConfig {
	return apiconfig.ModelConfig{
		Args:          append([]string(nil), config.Args...),
		ModelOverride: copyModelOverride(config.ModelOverride),
	}
}

func copyModelOverride(override *apiconfig.ModelOverride) *apiconfig.ModelOverride {
	if override == nil {
		return nil
	}
	copy := *override
	return &copy
}

func modelArgsEqual(a, b ModelArgs) bool {
	return reflect.DeepEqual(a, b)
}

// activeDeploymentChanged reports whether the currently assigned deployment
// changed. Inactive-model edits do not count. If the assigned model was removed
// from local support, assignedModelRemoved is true and changed is false so DAPI
// waits for a new chain assignment instead of deploying a fallback.
func activeDeploymentChanged(
	epochMLNodes map[string]types.MLNodeInfo,
	oldModels map[string]ModelArgs,
	newModels map[string]ModelArgs,
) (changed bool, assignedModelRemoved bool) {
	oldID, oldOK := ResolveNodeModelID(epochMLNodes, oldModels)
	newID, newOK := ResolveNodeModelID(epochMLNodes, newModels)
	if oldOK && !newOK {
		return false, true
	}
	if !newOK {
		return false, false
	}
	if !oldOK || oldID != newID {
		return true, false
	}
	return !modelArgsEqual(oldModels[oldID], newModels[newID]), false
}

func (b *Broker) refreshDeploymentUpdatePendingFromApplied(nodeID string) {
	if b.configManager == nil {
		return
	}
	b.mu.RLock()
	node, exists := b.nodes[nodeID]
	if !exists {
		b.mu.RUnlock()
		return
	}
	nodeModels := make(map[string]ModelArgs, len(node.Node.Models))
	for id, config := range node.Node.Models {
		nodeModels[id] = modelArgsFromConfig(modelConfigFromArgs(config))
	}
	epochNodes := make(map[string]types.MLNodeInfo, len(node.State.EpochMLNodes))
	for id, info := range node.State.EpochMLNodes {
		epochNodes[id] = info
	}
	epochModels := make(map[string]types.Model, len(node.State.EpochModels))
	for id, model := range node.State.EpochModels {
		epochModels[id] = model
	}
	b.mu.RUnlock()

	modelID, ok := b.resolveSupportedNodeModelID(epochNodes, nodeModels)
	if !ok {
		return
	}
	local := nodeModels[modelID]
	model, ok := epochModels[modelID]
	if !ok {
		return
	}
	expected := b.ResolveModelDeployment(model, local)
	applied, found, err := b.configManager.GetAppliedDeployment(context.Background(), nodeID)
	if err != nil {
		logging.Warn("Failed to load applied model deployment", types.Config,
			"node_id", nodeID, "model_id", modelID, "error", err)
		return
	}

	if !found ||
		applied.ModelID != expected.GovernanceID ||
		applied.Fingerprint != expected.Fingerprint() {
		b.markDeploymentUpdatePending(nodeID)
	}
}

func (b *Broker) markDeploymentUpdatePending(nodeID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if current, ok := b.nodes[nodeID]; ok {
		current.State.DeploymentUpdatePending = true
		current.State.DeploymentRetryAfter = time.Time{}
	}
}
