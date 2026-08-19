package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"cosmossdk.io/collections"
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

// non-accelerator hardware stripped from the GPU profile
var nonGpuHardwareTypes = map[string]struct{}{
	"CPU": {}, "RAM": {}, "MEMORY": {}, "DISK": {}, "SSD": {}, "HDD": {},
	"STORAGE": {}, "NVME": {}, "NIC": {}, "NETWORK": {},
}

func CanonicalGpuProfileId(node *types.HardwareNode) string {
	counts := make(map[string]uint32)
	for _, h := range node.GetHardware() {
		typ := strings.Join(strings.Fields(strings.ToUpper(strings.TrimSpace(h.GetType()))), " ")
		if typ == "" {
			continue
		}
		if _, skip := nonGpuHardwareTypes[typ]; skip {
			continue
		}
		counts[typ] += h.GetCount()
	}
	typeKeys := make([]string, 0, len(counts))
	for typ := range counts {
		typeKeys = append(typeKeys, typ)
	}
	sort.Strings(typeKeys)
	parts := make([]string, len(typeKeys))
	for i, typ := range typeKeys {
		parts[i] = fmt.Sprintf("%s x%d", typ, counts[typ])
	}
	return strings.Join(parts, " | ")
}

// IsNodeReserved checks whether a node is held by training: either reserved by
// an active shard or still inside its return buffer after release
func (k Keeper) IsNodeReserved(ctx context.Context, participant, nodeId string) bool {
	has, err := k.TrainshardReservations.Has(ctx, collections.Join(participant, nodeId))
	if err != nil {
		k.LogError("IsNodeReserved lookup failed, failing closed", types.Training,
			"participant", participant, "node_id", nodeId, "error", err)
		return true
	}
	return has
}

// IsNodeActivelyReserved checks whether a shard still runs on the node. A node
// inside its return buffer is not actively reserved, so the host may change it.
func (k Keeper) IsNodeActivelyReserved(ctx context.Context, participant, nodeId string) bool {
	shardId, err := k.TrainshardReservations.Get(ctx, collections.Join(participant, nodeId))
	if errors.Is(err, collections.ErrNotFound) {
		return false
	}
	if err == nil {
		var shard types.Trainshard
		if shard, err = k.Trainshards.Get(ctx, shardId); errors.Is(err, collections.ErrNotFound) {
			return false
		}
		if err == nil {
			return trainshardHasActiveNode(&shard, participant, nodeId)
		}
	}
	k.LogError("IsNodeActivelyReserved lookup failed, failing closed", types.Training,
		"participant", participant, "node_id", nodeId, "error", err)
	return true
}

// HasActiveTrainReservation checks whether a host has a node a shard still runs
// on. Nodes inside the return buffer do not block identity or stake changes.
func (k Keeper) HasActiveTrainReservation(ctx context.Context, participant string) bool {
	rng := collections.NewPrefixedPairRange[string, string](participant)
	var reserved bool
	if err := k.TrainshardReservations.Walk(ctx, rng, func(key collections.Pair[string, string], shardId uint64) (bool, error) {
		shard, err := k.Trainshards.Get(ctx, shardId)
		if errors.Is(err, collections.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if trainshardHasActiveNode(&shard, participant, key.K2()) {
			reserved = true
			return true, nil
		}
		return false, nil
	}); err != nil {
		k.LogError("HasActiveTrainReservation walk failed, failing closed", types.Training,
			"participant", participant, "error", err)
		return true
	}
	return reserved
}

func trainshardHasActiveNode(shard *types.Trainshard, participant, nodeId string) bool {
	for _, n := range shard.Nodes {
		if n.Participant == participant && n.NodeId == nodeId && isActiveReservedNode(n) {
			return true
		}
	}
	return false
}

func isActiveReservedNode(n *types.TrainshardReservedNode) bool {
	return n.Status == types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
}

func (k Keeper) IsModelUsedByActiveTrainshard(ctx context.Context, modelId string) bool {
	var used bool
	if err := k.TrainshardActiveIndex.Walk(ctx, nil, func(id uint64) (bool, error) {
		shard, err := k.Trainshards.Get(ctx, id)
		if err != nil {
			return false, err
		}
		for _, n := range shard.Nodes {
			if n.ModelId == modelId && isActiveReservedNode(n) {
				used = true
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		k.LogError("IsModelUsedByActiveTrainshard walk failed, failing closed", types.Training,
			"model_id", modelId, "error", err)
		return true
	}
	return used
}

func (k Keeper) trainingGuardianSet(ctx context.Context) map[string]bool {
	addresses := k.GetGenesisGuardianAddresses(ctx)
	set := make(map[string]bool, len(addresses))
	for _, addr := range addresses {
		accAddr, err := utils.OperatorAddressToAccAddress(addr)
		if err != nil {
			continue
		}
		set[accAddr] = true
	}
	return set
}

type trainingEpochNode struct {
	participant string
	nodeId      string
	profileId   string
	entries     []*types.TrainshardReservedNode
}

type trainingEpochView struct {
	nodes           map[string]*trainingEpochNode
	modelCapacity   map[string]int
	profileCapacity map[string]int
}

func nodeKey(participant, nodeId string) string {
	return participant + "\x00" + nodeId
}

func (k Keeper) buildTrainingEpochView(ctx context.Context) (*trainingEpochView, error) {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, types.ErrEffectiveEpochNotFound
	}
	active, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return nil, types.ErrEffectiveEpochNotFound
	}

	view := &trainingEpochView{
		nodes:           make(map[string]*trainingEpochNode),
		modelCapacity:   make(map[string]int),
		profileCapacity: make(map[string]int),
	}
	countedProfile := make(map[string]map[string]bool)
	countedModel := make(map[string]map[string]bool)

	for _, p := range active.Participants {
		if p == nil || p.Index == "" {
			continue
		}
		hardware, _ := k.GetHardwareNodes(ctx, p.Index)
		profileByLocalId := make(map[string]string)
		if hardware != nil {
			for _, hn := range hardware.HardwareNodes {
				profileByLocalId[hn.GetLocalId()] = CanonicalGpuProfileId(hn)
			}
		}
		for i, model := range p.Models {
			if i >= len(p.MlNodes) || p.MlNodes[i] == nil {
				continue
			}
			for _, ml := range p.MlNodes[i].MlNodes {
				// an id-less node cannot be opted in or reserved, and the other
				// epoch views skip it, so it must not add training capacity here
				if ml == nil || ml.NodeId == "" {
					continue
				}
				key := nodeKey(p.Index, ml.NodeId)
				// capacity must count physical nodes, so a node id repeated in
				// the model group adds neither an entry nor headroom
				if countedModel[model] == nil {
					countedModel[model] = make(map[string]bool)
				}
				if countedModel[model][key] {
					continue
				}
				countedModel[model][key] = true
				node := view.nodes[key]
				if node == nil {
					node = &trainingEpochNode{
						participant: p.Index,
						nodeId:      ml.NodeId,
						profileId:   profileByLocalId[ml.NodeId],
					}
					view.nodes[key] = node
				}
				node.entries = append(node.entries, &types.TrainshardReservedNode{
					Participant: p.Index,
					NodeId:      ml.NodeId,
					ModelId:     model,
					PocWeight:   ml.PocWeight,
				})
				view.modelCapacity[model]++
				if node.profileId != "" {
					if countedProfile[node.profileId] == nil {
						countedProfile[node.profileId] = make(map[string]bool)
					}
					if !countedProfile[node.profileId][key] {
						countedProfile[node.profileId][key] = true
						view.profileCapacity[node.profileId]++
					}
				}
			}
		}
	}
	return view, nil
}

type trainingReservedCounts struct {
	total         int
	perModel      map[string]int
	perProfile    map[string]int
	activeShards  int
	perCreatorAct map[string]int
}

func (k Keeper) buildTrainingReservedCounts(ctx context.Context) (*trainingReservedCounts, error) {
	counts := &trainingReservedCounts{
		perModel:      make(map[string]int),
		perProfile:    make(map[string]int),
		perCreatorAct: make(map[string]int),
	}
	err := k.TrainshardActiveIndex.Walk(ctx, nil, func(id uint64) (bool, error) {
		shard, err := k.Trainshards.Get(ctx, id)
		if err != nil {
			return false, err
		}
		counts.activeShards++
		counts.perCreatorAct[shard.Creator]++
		seen := make(map[string]bool)
		for _, n := range shard.Nodes {
			if !isActiveReservedNode(n) {
				continue
			}
			counts.perModel[n.ModelId]++
			key := nodeKey(n.Participant, n.NodeId)
			if !seen[key] {
				seen[key] = true
				counts.total++
				counts.perProfile[shard.GpuProfileId]++
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

func candidateSortKey(trainshardId uint64, participant, nodeId string) [32]byte {
	buf := make([]byte, 8, 8+len(participant)+1+len(nodeId))
	binary.BigEndian.PutUint64(buf, trainshardId)
	buf = append(buf, []byte(participant)...)
	buf = append(buf, 0)
	buf = append(buf, []byte(nodeId)...)
	return sha256.Sum256(buf)
}

func (k Keeper) selectTrainshardNodes(
	ctx context.Context,
	trainshardId uint64,
	gpuProfileId string,
	maxNodes uint32,
	params *types.TrainingParams,
) ([]*types.TrainshardReservedNode, error) {
	view, err := k.buildTrainingEpochView(ctx)
	if err != nil {
		return nil, err
	}
	reserved, err := k.buildTrainingReservedCounts(ctx)
	if err != nil {
		return nil, err
	}
	guardians := k.trainingGuardianSet(ctx)
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()

	candidates := make([]*trainingEpochNode, 0, len(view.nodes))
	for _, node := range view.nodes {
		if node.profileId == "" || node.profileId != gpuProfileId {
			continue
		}
		if guardians[node.participant] {
			continue
		}
		opted, err := k.hasLiveTrainingOptIn(ctx, node.participant, node.nodeId, height)
		if err != nil {
			return nil, err
		}
		if !opted {
			continue
		}
		if k.IsNodeReserved(ctx, node.participant, node.nodeId) {
			continue
		}
		candidates = append(candidates, node)
	}

	sort.Slice(candidates, func(i, j int) bool {
		ki := candidateSortKey(trainshardId, candidates[i].participant, candidates[i].nodeId)
		kj := candidateSortKey(trainshardId, candidates[j].participant, candidates[j].nodeId)
		if c := bytes.Compare(ki[:], kj[:]); c != 0 {
			return c < 0
		}
		if candidates[i].participant != candidates[j].participant {
			return candidates[i].participant < candidates[j].participant
		}
		return candidates[i].nodeId < candidates[j].nodeId
	})

	modelCap := func(model string) int {
		return reserveShareCap(view.modelCapacity[model], params.MaxReservedSharePerModelBps)
	}
	profileCap := reserveShareCap(view.profileCapacity[gpuProfileId], params.MaxReservedSharePerProfileBps)

	takenModel := make(map[string]int)
	takenProfile := 0
	takenTotal := 0
	picked := make([]*types.TrainshardReservedNode, 0, maxNodes)

	for _, node := range candidates {
		if uint32(takenTotal) >= maxNodes {
			break
		}
		if reserved.total+takenTotal+1 > int(params.MaxTotalReservedNodes) {
			break
		}
		if reserved.perProfile[gpuProfileId]+takenProfile+1 > profileCap {
			continue
		}
		if view.profileCapacity[gpuProfileId]-(reserved.perProfile[gpuProfileId]+takenProfile+1) < 0 {
			continue
		}
		fits := true
		for _, e := range node.entries {
			if reserved.perModel[e.ModelId]+takenModel[e.ModelId]+1 > modelCap(e.ModelId) {
				fits = false
				break
			}
			if view.modelCapacity[e.ModelId]-(reserved.perModel[e.ModelId]+takenModel[e.ModelId]+1) < 1 {
				fits = false
				break
			}
		}
		if !fits {
			continue
		}
		for _, e := range node.entries {
			takenModel[e.ModelId]++
			e.Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
			picked = append(picked, e)
		}
		takenProfile++
		takenTotal++
	}

	if uint32(takenTotal) < maxNodes {
		return nil, types.ErrTrainshardCapacity
	}
	return picked, nil
}

func reserveShareCap(capacity int, shareBps uint32) int {
	return capacity * int(shareBps) / 10000
}

func (k Keeper) hasLiveTrainingOptIn(ctx context.Context, participant, nodeId string, height int64) (bool, error) {
	expiresAt, err := k.TrainingNodeOptIns.Get(ctx, collections.Join(participant, nodeId))
	if errors.Is(err, collections.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return expiresAt > height, nil
}

func (k Keeper) setTrainingOptIn(ctx context.Context, participant, nodeId string, height int64) (int64, error) {
	expiresAt := height + k.GetTrainingParams(ctx).OptInTtlBlocks
	return expiresAt, k.TrainingNodeOptIns.Set(ctx, collections.Join(participant, nodeId), expiresAt)
}

func (k Keeper) reserveTrainshardNodes(ctx context.Context, shard *types.Trainshard) error {
	for _, n := range shard.Nodes {
		if err := k.TrainshardReservations.Set(ctx, collections.Join(n.Participant, n.NodeId), shard.TrainshardId); err != nil {
			return err
		}
	}
	if err := k.Trainshards.Set(ctx, shard.TrainshardId, *shard); err != nil {
		return err
	}
	if err := k.TrainshardActiveIndex.Set(ctx, shard.TrainshardId); err != nil {
		return err
	}
	return k.TrainshardExpiryIndex.Set(ctx, collections.Join(shard.ExpiresAtHeight, shard.TrainshardId))
}

// releaseTrainshardNodes marks selected active nodes released and keeps their
// reservation alive until the return buffer ends, when the EndBlocker drops it
func (k Keeper) releaseTrainshardNodes(
	ctx context.Context,
	shard *types.Trainshard,
	height int64,
	status types.TrainshardNodeStatus,
	reason string,
	selected func(n *types.TrainshardReservedNode) bool,
) (int, error) {
	until := height + k.GetTrainingParams(ctx).ReleaseBufferBlocks
	released := make(map[string]bool)
	for _, n := range shard.Nodes {
		if !isActiveReservedNode(n) || (selected != nil && !selected(n)) {
			continue
		}
		n.Status = status
		n.ReleasedAtHeight = height
		n.ReleaseReason = reason
		n.ReservedUntilHeight = until
		key := nodeKey(n.Participant, n.NodeId)
		if released[key] {
			continue
		}
		released[key] = true
		if err := k.TrainshardReleaseIndex.Set(ctx, collections.Join3(until, n.Participant, n.NodeId)); err != nil {
			return 0, err
		}
	}
	return len(released), nil
}

func (k Keeper) hasActiveTrainshardNodes(shard *types.Trainshard) bool {
	for _, n := range shard.Nodes {
		if isActiveReservedNode(n) {
			return true
		}
	}
	return false
}

func (k Keeper) closeTrainshard(
	ctx context.Context,
	shard *types.Trainshard,
	status types.TrainshardStatus,
	reason types.TrainshardCloseReason,
	closeHeight int64,
) error {
	if _, err := k.releaseTrainshardNodes(ctx, shard, closeHeight,
		types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE, reason.String(), nil); err != nil {
		return err
	}
	if err := k.TrainshardExpiryIndex.Remove(ctx, collections.Join(shard.ExpiresAtHeight, shard.TrainshardId)); err != nil {
		return err
	}
	if err := k.TrainshardActiveIndex.Remove(ctx, shard.TrainshardId); err != nil {
		return err
	}

	shard.Status = status
	shard.CloseReason = reason
	shard.ClosedAtHeight = closeHeight
	if err := k.Trainshards.Set(ctx, shard.TrainshardId, *shard); err != nil {
		return err
	}
	if err := k.TrainshardClosedIndex.Set(ctx, collections.Join(closeHeight, shard.TrainshardId)); err != nil {
		return err
	}
	return k.advanceCreatorCooldown(ctx, shard.Creator, closeHeight)
}

func (k Keeper) advanceCreatorCooldown(ctx context.Context, creator string, closeHeight int64) error {
	params := k.GetTrainingParams(ctx)
	until := closeHeight + params.CreatorCooldownBlocks
	existing, err := k.TrainshardCreatorCooldown.Get(ctx, creator)
	if err == nil && existing >= until {
		return nil
	}
	return k.TrainshardCreatorCooldown.Set(ctx, creator, until)
}

func (k Keeper) creatorCooldownUntil(ctx context.Context, creator string) int64 {
	until, err := k.TrainshardCreatorCooldown.Get(ctx, creator)
	if err != nil {
		return 0
	}
	return until
}

func (k Keeper) nextTrainshardId(ctx context.Context) (uint64, error) {
	current, err := k.TrainshardCounter.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 1, nil
		}
		return 0, err
	}
	return current + 1, nil
}

func (k Keeper) nextTrainshardProposalId(ctx context.Context) (uint64, error) {
	current, err := k.TrainshardProposalCounter.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return 1, nil
		}
		return 0, err
	}
	return current + 1, nil
}

func emitTrainshardEvent(ctx context.Context, eventType string, attrs ...sdk.Attribute) {
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(eventType, attrs...))
}

func (k Keeper) CollectReservedNodeIds(ctx context.Context) map[string]map[string]struct{} {
	reserved := make(map[string]map[string]struct{})
	if err := k.TrainshardReservations.Walk(ctx, nil, func(key collections.Pair[string, string], _ uint64) (bool, error) {
		participant, nodeId := key.K1(), key.K2()
		set, ok := reserved[participant]
		if !ok {
			set = make(map[string]struct{})
			reserved[participant] = set
		}
		set[nodeId] = struct{}{}
		return false, nil
	}); err != nil {
		k.LogError("CollectReservedNodeIds walk failed", types.Training, "error", err)
	}
	return reserved
}

func modelFreeWeight(p *types.ActiveParticipant, modelId string, reservedNodes map[string]struct{}) (int64, bool) {
	modelIndex := -1
	for i, m := range p.Models {
		if m == modelId {
			modelIndex = i
			break
		}
	}
	if modelIndex < 0 || modelIndex >= len(p.MlNodes) || p.MlNodes[modelIndex] == nil {
		return 0, false
	}

	weight := int64(0)
	counted := make(map[string]struct{})
	for _, node := range p.MlNodes[modelIndex].MlNodes {
		if node == nil || node.NodeId == "" {
			continue
		}
		if _, seen := counted[node.NodeId]; seen {
			continue
		}
		counted[node.NodeId] = struct{}{}
		if _, reserved := reservedNodes[node.NodeId]; reserved {
			continue
		}
		weight += node.PocWeight
	}

	return weight, true
}

func (k Keeper) collectModelFreeWeights(ctx context.Context, epochId uint64, modelId string) map[string]int64 {
	reserved := k.CollectReservedNodeIds(ctx)
	if len(reserved) == 0 {
		return nil
	}
	active, found := k.GetActiveParticipants(ctx, epochId)
	if !found {
		return nil
	}

	weights := make(map[string]int64)
	for _, p := range active.Participants {
		if p == nil || p.Index == "" {
			continue
		}
		reservedNodes := reserved[p.Index]
		if len(reservedNodes) == 0 {
			continue
		}
		if freeWeight, tracked := modelFreeWeight(p, modelId, reservedNodes); tracked {
			weights[p.Index] = freeWeight
		}
	}
	return weights
}

func (k Keeper) CollectEpochRawPocWeights(ctx context.Context, epochIndex uint64) map[string]int64 {
	active, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return nil
	}
	weights := make(map[string]int64, len(active.Participants))
	for _, p := range active.Participants {
		if p == nil || p.Index == "" {
			continue
		}
		weights[p.Index] = rawPocWeight(p)
	}
	return weights
}

func rawPocWeight(p *types.ActiveParticipant) int64 {
	counted := make(map[string]int64)
	for _, group := range p.MlNodes {
		if group == nil {
			continue
		}
		for _, node := range group.MlNodes {
			if node == nil || node.NodeId == "" {
				continue
			}
			if node.PocWeight > counted[node.NodeId] {
				counted[node.NodeId] = node.PocWeight
			}
		}
	}
	weight := int64(0)
	for _, w := range counted {
		weight += w
	}
	return weight
}

// FreeShareOfWeight drops the reserved part of a stored weight. Reserved totals
// are raw PoC weight while stored weights are already coefficient-, cap- and
// collateral-adjusted, so the reserved part is removed as a share of the host's
// raw weight instead of being subtracted directly. Without a raw weight to
// compare against we cannot size the share, so the whole weight is dropped.
func FreeShareOfWeight(weight, reservedRaw, totalRaw int64) int64 {
	if weight <= 0 {
		return 0
	}
	if reservedRaw <= 0 {
		return weight
	}
	if totalRaw <= 0 || reservedRaw >= totalRaw {
		return 0
	}
	return sdkmath.LegacyNewDec(weight).MulInt64(totalRaw - reservedRaw).QuoInt64(totalRaw).TruncateInt64()
}

func (k Keeper) epochBlockRange(ctx context.Context, epochIndex uint64) (int64, int64) {
	epoch, found := k.GetEpoch(ctx, epochIndex)
	if !found {
		return 0, int64(math.MaxInt64)
	}
	end := int64(math.MaxInt64)
	if next, ok := k.GetEpoch(ctx, epochIndex+1); ok && next.PocStartBlockHeight > 0 {
		end = next.PocStartBlockHeight - 1
	}
	return epoch.PocStartBlockHeight, end
}

type ReservationScope int

const (
	// ReservationScopeReward ends the window at the release height, so the
	// return buffer never costs the node the next epoch's rewards
	ReservationScopeReward ReservationScope = iota
	// ReservationScopeShield extends the window over the return buffer, which
	// routing, penalties and cPoC read
	ReservationScopeShield
)

// trainshardNodeWindow returns the height range in which the node counted as
// reserved: an active node has no end yet, a released one keeps its frozen
// heights so the window never depends on current params
func trainshardNodeWindow(shard *types.Trainshard, n *types.TrainshardReservedNode, scope ReservationScope) (int64, int64) {
	if isActiveReservedNode(n) {
		return shard.CreatedAtHeight, int64(math.MaxInt64)
	}
	end := n.ReleasedAtHeight
	if scope == ReservationScopeShield && n.ReservedUntilHeight > end {
		end = n.ReservedUntilHeight
	}
	if end == 0 {
		end = shard.ClosedAtHeight
	}
	if end == 0 {
		end = int64(math.MaxInt64)
	}
	return shard.CreatedAtHeight, end
}

func (k Keeper) forEachEpochReservedNode(ctx context.Context, epochIndex uint64, scope ReservationScope, fn func(n *types.TrainshardReservedNode, start, end int64)) {
	epochStart, epochEnd := k.epochBlockRange(ctx, epochIndex)
	if err := k.Trainshards.Walk(ctx, nil, func(_ uint64, shard types.Trainshard) (bool, error) {
		for _, n := range shard.Nodes {
			start, end := trainshardNodeWindow(&shard, n, scope)
			if start > epochEnd || end < epochStart {
				continue
			}
			if start < epochStart {
				start = epochStart
			}
			if end > epochEnd {
				end = epochEnd
			}
			fn(n, start, end)
		}
		return false, nil
	}); err != nil {
		k.LogError("epoch reserved node walk failed", types.Training, "error", err)
	}
}

func (k Keeper) CollectEpochReservedNodeIds(ctx context.Context, epochIndex uint64, scope ReservationScope) map[string]map[string]struct{} {
	result := make(map[string]map[string]struct{})
	k.forEachEpochReservedNode(ctx, epochIndex, scope, func(n *types.TrainshardReservedNode, _, _ int64) {
		set, ok := result[n.Participant]
		if !ok {
			set = make(map[string]struct{})
			result[n.Participant] = set
		}
		set[n.NodeId] = struct{}{}
	})
	return result
}

func (k Keeper) CollectEpochReservedNodeWeights(ctx context.Context, epochIndex uint64, scope ReservationScope) map[string][]*types.TrainshardReservedNode {
	return k.collectEpochReservedNodeWeights(ctx, epochIndex, scope, nil)
}

func (k Keeper) CollectEpochReservedNodeWeightsAtHeight(ctx context.Context, epochIndex uint64, height int64, scope ReservationScope) map[string][]*types.TrainshardReservedNode {
	return k.collectEpochReservedNodeWeights(ctx, epochIndex, scope, &height)
}

func (k Keeper) collectEpochReservedNodeWeights(ctx context.Context, epochIndex uint64, scope ReservationScope, height *int64) map[string][]*types.TrainshardReservedNode {
	return k.BuildEpochReservedWeightView(ctx, epochIndex, scope).nodeWeights(height)
}

type EpochReservedWeightView struct {
	windows []reservedNodeWindow
}

type reservedNodeWindow struct {
	node  *types.TrainshardReservedNode
	start int64
	end   int64
}

func (k Keeper) BuildEpochReservedWeightView(ctx context.Context, epochIndex uint64, scope ReservationScope) EpochReservedWeightView {
	var view EpochReservedWeightView
	k.forEachEpochReservedNode(ctx, epochIndex, scope, func(n *types.TrainshardReservedNode, start, end int64) {
		view.windows = append(view.windows, reservedNodeWindow{node: n, start: start, end: end})
	})
	return view
}

func (v EpochReservedWeightView) TotalsAt(height int64) (byModelHost map[string]map[string]int64, byHost map[string]int64) {
	return aggregateReservedWeightTotals(v.nodeWeights(&height))
}

func (v EpochReservedWeightView) nodeWeights(height *int64) map[string][]*types.TrainshardReservedNode {
	type modelNode struct{ model, nodeId string }
	seen := make(map[string]map[modelNode]int64)
	for _, w := range v.windows {
		n := w.node
		if height != nil && (*height < w.start || *height > w.end) {
			continue
		}
		set, ok := seen[n.Participant]
		if !ok {
			set = make(map[modelNode]int64)
			seen[n.Participant] = set
		}
		mk := modelNode{model: n.ModelId, nodeId: n.NodeId}
		if prev, exists := set[mk]; !exists || n.PocWeight > prev {
			set[mk] = n.PocWeight
		}
	}
	result := make(map[string][]*types.TrainshardReservedNode, len(seen))
	for participant, nodes := range seen {
		for mn, weight := range nodes {
			result[participant] = append(result[participant], &types.TrainshardReservedNode{
				Participant: participant,
				ModelId:     mn.model,
				NodeId:      mn.nodeId,
				PocWeight:   weight,
			})
		}
	}
	return result
}

func (k Keeper) CollectEpochReservedWeightTotals(ctx context.Context, epochIndex uint64, scope ReservationScope) (byModelHost map[string]map[string]int64, byHost map[string]int64) {
	return aggregateReservedWeightTotals(k.CollectEpochReservedNodeWeights(ctx, epochIndex, scope))
}

func (k Keeper) CollectEpochReservedWeightTotalsAtHeight(ctx context.Context, epochIndex uint64, height int64, scope ReservationScope) (byModelHost map[string]map[string]int64, byHost map[string]int64) {
	return aggregateReservedWeightTotals(k.CollectEpochReservedNodeWeightsAtHeight(ctx, epochIndex, height, scope))
}

func aggregateReservedWeightTotals(reserved map[string][]*types.TrainshardReservedNode) (byModelHost map[string]map[string]int64, byHost map[string]int64) {
	byModelHost = make(map[string]map[string]int64)
	byHost = make(map[string]int64)
	for host, nodes := range reserved {
		// byModelHost counts per model; byHost dedups node ids
		nodeWeight := make(map[string]int64, len(nodes))
		for _, n := range nodes {
			if byModelHost[n.ModelId] == nil {
				byModelHost[n.ModelId] = make(map[string]int64)
			}
			byModelHost[n.ModelId][host] += n.PocWeight
			if n.PocWeight > nodeWeight[n.NodeId] {
				nodeWeight[n.NodeId] = n.PocWeight
			}
		}
		for _, w := range nodeWeight {
			byHost[host] += w
		}
	}
	return byModelHost, byHost
}

type hostReservedInterval struct {
	start  int64
	end    int64
	nodeId string
}

func (k Keeper) collectEpochReservedIntervals(ctx context.Context, epochIndex uint64) map[string][]hostReservedInterval {
	result := make(map[string][]hostReservedInterval)
	seen := make(map[string]map[hostReservedInterval]bool)
	k.forEachEpochReservedNode(ctx, epochIndex, ReservationScopeShield, func(n *types.TrainshardReservedNode, start, end int64) {
		interval := hostReservedInterval{start: start, end: end, nodeId: n.NodeId}
		if seen[n.Participant] == nil {
			seen[n.Participant] = make(map[hostReservedInterval]bool)
		}
		if seen[n.Participant][interval] {
			return
		}
		seen[n.Participant][interval] = true
		result[n.Participant] = append(result[n.Participant], interval)
	})
	return result
}

func hostFullyReservedAtHeight(epochNodes map[string]struct{}, intervals []hostReservedInterval, height int64) bool {
	if len(epochNodes) == 0 || len(intervals) == 0 {
		return false
	}
	covered := make(map[string]struct{}, len(epochNodes))
	for _, iv := range intervals {
		if iv.start > height || iv.end < height {
			continue
		}
		if _, want := epochNodes[iv.nodeId]; want {
			covered[iv.nodeId] = struct{}{}
		}
	}
	return len(covered) == len(epochNodes)
}

func hostEpochNodeSet(p *types.ActiveParticipant) map[string]struct{} {
	nodes := make(map[string]struct{})
	for i := range p.Models {
		if i >= len(p.MlNodes) || p.MlNodes[i] == nil {
			continue
		}
		for _, n := range p.MlNodes[i].MlNodes {
			if n != nil && n.NodeId != "" {
				nodes[n.NodeId] = struct{}{}
			}
		}
	}
	return nodes
}

type EpochReservationView struct {
	intervals map[string][]hostReservedInterval
	nodes     map[string]map[string]struct{}
}

func (k Keeper) BuildEpochReservationView(ctx context.Context, epochIndex uint64) EpochReservationView {
	v := EpochReservationView{
		intervals: k.collectEpochReservedIntervals(ctx, epochIndex),
		nodes:     make(map[string]map[string]struct{}),
	}
	if active, found := k.GetActiveParticipants(ctx, epochIndex); found {
		for _, p := range active.Participants {
			if p != nil {
				v.nodes[p.Index] = hostEpochNodeSet(p)
			}
		}
	}
	return v
}

func (v EpochReservationView) FullyReservedAt(host string, height int64) bool {
	return hostFullyReservedAtHeight(v.nodes[host], v.intervals[host], height)
}

func (k Keeper) CollectEpochReservedHostsForModel(ctx context.Context, epochIndex uint64, modelId string, scope ReservationScope) map[string]struct{} {
	hosts := make(map[string]struct{})
	k.forEachEpochReservedNode(ctx, epochIndex, scope, func(n *types.TrainshardReservedNode, _, _ int64) {
		if n != nil && n.ModelId == modelId && n.Participant != "" {
			hosts[n.Participant] = struct{}{}
		}
	})
	return hosts
}

func (k Keeper) CollectEpochFullyReservedHostsForModel(ctx context.Context, epochIndex uint64, modelId string) map[string]struct{} {
	fullyReserved := make(map[string]struct{})
	intervals := k.collectEpochReservedIntervals(ctx, epochIndex)
	if len(intervals) == 0 {
		return fullyReserved
	}
	epochStart, epochEnd := k.epochBlockRange(ctx, epochIndex)
	active, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return fullyReserved
	}
	for _, p := range active.Participants {
		if p == nil {
			continue
		}
		modelNodes := modelNodeSet(p, modelId)
		if len(modelNodes) > 0 && hostFullyReservedForModelWindow(modelNodes, intervals[p.Index], epochStart, epochEnd) {
			fullyReserved[p.Index] = struct{}{}
		}
	}
	return fullyReserved
}

func modelNodeSet(p *types.ActiveParticipant, modelId string) map[string]struct{} {
	for i, m := range p.Models {
		if m != modelId {
			continue
		}
		if i >= len(p.MlNodes) || p.MlNodes[i] == nil {
			return nil
		}
		set := make(map[string]struct{}, len(p.MlNodes[i].MlNodes))
		for _, n := range p.MlNodes[i].MlNodes {
			if n != nil && n.NodeId != "" {
				set[n.NodeId] = struct{}{}
			}
		}
		return set
	}
	return nil
}

// hostFullyReservedForModelWindow checks that all model nodes stayed reserved
// across the whole epoch window. Coverage only changes where an interval starts
// or ends, so probing those checkpoints is enough.
func hostFullyReservedForModelWindow(modelNodes map[string]struct{}, intervals []hostReservedInterval, epochStart, epochEnd int64) bool {
	if len(modelNodes) == 0 || len(intervals) == 0 || epochStart > epochEnd {
		return false
	}
	checkpoints := make([]int64, 0, len(intervals)*2+1)
	checkpoints = append(checkpoints, epochStart)
	for _, iv := range intervals {
		if iv.end < epochStart || iv.start > epochEnd {
			continue
		}
		if iv.start > epochStart {
			checkpoints = append(checkpoints, iv.start)
		}
		if iv.end < epochEnd {
			checkpoints = append(checkpoints, iv.end+1)
		}
	}
	sort.Slice(checkpoints, func(i, j int) bool { return checkpoints[i] < checkpoints[j] })
	for i, checkpoint := range checkpoints {
		if i > 0 && checkpoint == checkpoints[i-1] {
			continue
		}
		if !hostFullyReservedAtHeight(modelNodes, intervals, checkpoint) {
			return false
		}
	}
	return true
}
