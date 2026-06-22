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

// IsNodeReserved checks whether a node is reserved by an active shard
func (k Keeper) IsNodeReserved(ctx context.Context, participant, nodeId string) bool {
	has, err := k.TrainshardReservations.Has(ctx, collections.Join(participant, nodeId))
	if err != nil {
		k.LogError("IsNodeReserved lookup failed, failing closed", types.Training,
			"participant", participant, "node_id", nodeId, "error", err)
		return true
	}
	return has
}

// HasActiveTrainReservation checks whether a host has any reserved node
func (k Keeper) HasActiveTrainReservation(ctx context.Context, participant string) bool {
	rng := collections.NewPrefixedPairRange[string, string](participant)
	var reserved bool
	if err := k.TrainshardReservations.Walk(ctx, rng, func(_ collections.Pair[string, string], _ uint64) (bool, error) {
		reserved = true
		return true, nil
	}); err != nil {
		k.LogError("HasActiveTrainReservation walk failed, failing closed", types.Training,
			"participant", participant, "error", err)
		return true
	}
	return reserved
}

// IsModelUsedByActiveTrainshard checks whether the model has any active shard
func (k Keeper) IsModelUsedByActiveTrainshard(ctx context.Context, modelId string) bool {
	var used bool
	if err := k.TrainshardActiveIndex.Walk(ctx, nil, func(id uint64) (bool, error) {
		shard, err := k.Trainshards.Get(ctx, id)
		if err != nil {
			return false, err
		}
		for _, n := range shard.Nodes {
			if n.ModelId == modelId {
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

	for _, p := range active.Participants {
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
				key := nodeKey(p.Index, ml.NodeId)
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

// selectTrainshardNodes deterministically picks nodes that honor caps
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

	candidates := make([]*trainingEpochNode, 0, len(view.nodes))
	for _, node := range view.nodes {
		if node.profileId == "" || node.profileId != gpuProfileId {
			continue
		}
		if guardians[node.participant] {
			continue
		}
		opted, err := k.TrainingNodeOptIns.Has(ctx, collections.Join(node.participant, node.nodeId))
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

func (k Keeper) closeTrainshard(ctx context.Context, shard *types.Trainshard, status types.TrainshardStatus, closeHeight int64) error {
	seen := make(map[string]bool)
	for _, n := range shard.Nodes {
		key := nodeKey(n.Participant, n.NodeId)
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := k.TrainshardReservations.Remove(ctx, collections.Join(n.Participant, n.NodeId)); err != nil {
			return err
		}
	}
	if err := k.TrainshardExpiryIndex.Remove(ctx, collections.Join(shard.ExpiresAtHeight, shard.TrainshardId)); err != nil {
		return err
	}
	if err := k.TrainshardActiveIndex.Remove(ctx, shard.TrainshardId); err != nil {
		return err
	}

	shard.Status = status
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

// CollectReservedNodeIds returns active reserved node ids per host
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

// epochBlockRange returns the epoch block range
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

func trainshardOverlapsRange(shard *types.Trainshard, start, end int64) bool {
	shardEnd := shard.ClosedAtHeight
	if shard.Status == types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE || shardEnd == 0 {
		shardEnd = int64(math.MaxInt64)
	}
	return shard.CreatedAtHeight <= end && shardEnd >= start
}

// forEachEpochReservedShard walks shards that overlap the epoch
func (k Keeper) forEachEpochReservedShard(ctx context.Context, epochIndex uint64, fn func(shard *types.Trainshard, start, end int64)) {
	start, end := k.epochBlockRange(ctx, epochIndex)
	if err := k.Trainshards.Walk(ctx, nil, func(_ uint64, shard types.Trainshard) (bool, error) {
		if !trainshardOverlapsRange(&shard, start, end) {
			return false, nil
		}
		s := shard.CreatedAtHeight
		if s < start {
			s = start
		}
		e := shard.ClosedAtHeight
		if shard.Status == types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE || e == 0 || e > end {
			e = end
		}
		fn(&shard, s, e)
		return false, nil
	}); err != nil {
		k.LogError("epoch reserved shard walk failed", types.Training, "error", err)
	}
}

// CollectEpochReservedNodeIds returns nodes reserved during the epoch
func (k Keeper) CollectEpochReservedNodeIds(ctx context.Context, epochIndex uint64) map[string]map[string]struct{} {
	result := make(map[string]map[string]struct{})
	k.forEachEpochReservedShard(ctx, epochIndex, func(shard *types.Trainshard, _, _ int64) {
		for _, n := range shard.Nodes {
			set, ok := result[n.Participant]
			if !ok {
				set = make(map[string]struct{})
				result[n.Participant] = set
			}
			set[n.NodeId] = struct{}{}
		}
	})
	return result
}

// CollectEpochReservedNodeWeights returns frozen reserved model-node weights
func (k Keeper) CollectEpochReservedNodeWeights(ctx context.Context, epochIndex uint64) map[string][]*types.TrainshardReservedNode {
	type modelNode struct{ model, nodeId string }
	seen := make(map[string]map[modelNode]int64)
	k.forEachEpochReservedShard(ctx, epochIndex, func(shard *types.Trainshard, _, _ int64) {
		for _, n := range shard.Nodes {
			set, ok := seen[n.Participant]
			if !ok {
				set = make(map[modelNode]int64)
				seen[n.Participant] = set
			}
			mk := modelNode{model: n.ModelId, nodeId: n.NodeId}
			if w, exists := set[mk]; !exists || n.PocWeight > w {
				set[mk] = n.PocWeight
			}
		}
	})
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

// CollectEpochReservedWeightTotals aggregates frozen reserved weight
func (k Keeper) CollectEpochReservedWeightTotals(ctx context.Context, epochIndex uint64) (byModelHost map[string]map[string]int64, byHost map[string]int64) {
	byModelHost = make(map[string]map[string]int64)
	byHost = make(map[string]int64)
	for host, nodes := range k.CollectEpochReservedNodeWeights(ctx, epochIndex) {
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
	start int64
	end   int64
	nodes map[string]struct{}
}

// collectEpochReservedIntervals returns reservation intervals per host
func (k Keeper) collectEpochReservedIntervals(ctx context.Context, epochIndex uint64) map[string][]hostReservedInterval {
	result := make(map[string][]hostReservedInterval)
	k.forEachEpochReservedShard(ctx, epochIndex, func(shard *types.Trainshard, s, e int64) {
		byParticipant := make(map[string]map[string]struct{})
		for _, n := range shard.Nodes {
			set, ok := byParticipant[n.Participant]
			if !ok {
				set = make(map[string]struct{})
				byParticipant[n.Participant] = set
			}
			set[n.NodeId] = struct{}{}
		}
		for p, nodes := range byParticipant {
			result[p] = append(result[p], hostReservedInterval{start: s, end: e, nodes: nodes})
		}
	})
	return result
}

// hostFullyReservedAtHeight reports whether a host had zero free nodes
func hostFullyReservedAtHeight(epochNodes map[string]struct{}, intervals []hostReservedInterval, height int64) bool {
	if len(epochNodes) == 0 || len(intervals) == 0 {
		return false
	}
	covered := make(map[string]struct{}, len(epochNodes))
	for _, iv := range intervals {
		if iv.start > height || iv.end < height {
			continue
		}
		for id := range iv.nodes {
			if _, want := epochNodes[id]; want {
				covered[id] = struct{}{}
			}
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

// EpochReservationView checks whether a host was fully reserved at a height
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

// FullyReservedAt reports whether the host had zero free nodes at height
func (v EpochReservationView) FullyReservedAt(host string, height int64) bool {
	return hostFullyReservedAtHeight(v.nodes[host], v.intervals[host], height)
}

// CollectEpochFullyReservedHostsForModel returns epoch-fully-reserved hosts
func (k Keeper) CollectEpochFullyReservedHostsForModel(ctx context.Context, epochIndex uint64, modelId string) map[string]struct{} {
	fullyReserved := make(map[string]struct{})
	intervals := k.collectEpochReservedIntervals(ctx, epochIndex)
	if len(intervals) == 0 {
		return fullyReserved
	}
	active, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return fullyReserved
	}
	for _, p := range active.Participants {
		if p == nil {
			continue
		}
		modelNodes := modelNodeSet(p, modelId)
		if len(modelNodes) > 0 && hostEverFullyReservedForModel(modelNodes, intervals[p.Index]) {
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

// hostEverFullyReservedForModel checks if all model nodes were reserved at once
func hostEverFullyReservedForModel(modelNodes map[string]struct{}, intervals []hostReservedInterval) bool {
	for _, probe := range intervals {
		if hostFullyReservedAtHeight(modelNodes, intervals, probe.start) {
			return true
		}
	}
	return false
}
