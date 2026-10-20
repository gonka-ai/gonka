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

	"cosmossdk.io/collections"
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

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
// inside its return buffer is not actively reserved.
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
	gpuType     string
	gpuCount    uint32
	endpoint    string
	entries     []*types.TrainshardReservedNode
}

type trainingEpochView struct {
	epochIndex      uint64
	nodes           map[string]*trainingEpochNode
	modelCapacity   map[string]int
	profileCapacity map[string]int
	weightRates     map[string]map[string]weightRate
}

type weightRate struct {
	median       sdkmath.LegacyDec
	participants int
}

type nodeHardware struct {
	profileId string
	gpuType   string
	gpuCount  uint32
	single    bool
}

type participantRate struct {
	weight   int64
	gpus     uint64
	gpuType  string
	eligible bool
}

func (r *participantRate) add(weight int64, hw nodeHardware) {
	if !hw.single || (r.gpuType != "" && r.gpuType != hw.gpuType) {
		r.eligible = false
		return
	}
	r.gpuType = hw.gpuType
	r.weight += weight
	r.gpus += uint64(hw.gpuCount)
}

func medianWeightRates(samples map[string]map[string][]sdkmath.LegacyDec) map[string]map[string]weightRate {
	rates := make(map[string]map[string]weightRate, len(samples))
	for model, byType := range samples {
		rates[model] = make(map[string]weightRate, len(byType))
		for gpuType, values := range byType {
			sort.Slice(values, func(i, j int) bool { return values[i].LT(values[j]) })
			mid := len(values) / 2
			median := values[mid]
			if len(values)%2 == 0 {
				median = values[mid-1].Add(values[mid]).QuoInt64(2)
			}
			rates[model][gpuType] = weightRate{median: median, participants: len(values)}
		}
	}
	return rates
}

func (v *trainingEpochView) capNodeWeights(node *trainingEpochNode, params *types.TrainingParams) bool {
	if node.gpuType == "" || node.gpuCount == 0 {
		return false
	}
	caps := make([]sdkmath.LegacyDec, len(node.entries))
	for i, e := range node.entries {
		rate, ok := v.weightRates[e.ModelId][node.gpuType]
		if !ok || rate.participants < int(params.MinWeightRateParticipants) {
			return false
		}
		caps[i] = rate.median.
			MulInt64(int64(node.gpuCount)).
			MulInt64(10000 + int64(params.NodeWeightToleranceBps)).
			QuoInt64(10000)
	}
	for i, e := range node.entries {
		if caps[i].LT(sdkmath.LegacyNewDec(e.PocWeight)) {
			e.PocWeight = caps[i].TruncateInt64()
		}
	}
	return true
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
		epochIndex:      epochIndex,
		nodes:           make(map[string]*trainingEpochNode),
		modelCapacity:   make(map[string]int),
		profileCapacity: make(map[string]int),
	}
	countedProfile := make(map[string]map[string]bool)
	countedModel := make(map[string]map[string]bool)
	rateSamples := make(map[string]map[string][]sdkmath.LegacyDec)

	for _, p := range active.Participants {
		if p == nil || p.Index == "" {
			continue
		}
		hardware, _ := k.GetHardwareNodes(ctx, p.Index)
		hardwareByLocalId := make(map[string]nodeHardware)
		if hardware != nil {
			for _, hn := range hardware.HardwareNodes {
				gpuType, gpuCount, single := types.SingleGpuTypeOf(hn.GetHardware())
				hardwareByLocalId[hn.GetLocalId()] = nodeHardware{
					profileId: types.CanonicalGpuProfileId(hn),
					gpuType:   gpuType,
					gpuCount:  gpuCount,
					single:    single,
				}
			}
		}
		for i, model := range p.Models {
			if i >= len(p.MlNodes) || p.MlNodes[i] == nil {
				continue
			}
			rate := participantRate{eligible: true}
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
				hw := hardwareByLocalId[ml.NodeId]
				rate.add(ml.PocWeight, hw)
				node := view.nodes[key]
				if node == nil {
					node = &trainingEpochNode{
						participant: p.Index,
						nodeId:      ml.NodeId,
						profileId:   hw.profileId,
						gpuType:     hw.gpuType,
						gpuCount:    hw.gpuCount,
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
			if rate.eligible && rate.gpus > 0 {
				if rateSamples[model] == nil {
					rateSamples[model] = make(map[string][]sdkmath.LegacyDec)
				}
				sample := sdkmath.LegacyNewDec(rate.weight).QuoInt64(int64(rate.gpus))
				rateSamples[model][rate.gpuType] = append(rateSamples[model][rate.gpuType], sample)
			}
		}
	}
	view.weightRates = medianWeightRates(rateSamples)
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
	if err := k.TrainshardActiveIndex.Walk(ctx, nil, func(id uint64) (bool, error) {
		shard, err := k.Trainshards.Get(ctx, id)
		if err != nil {
			return false, err
		}
		counts.activeShards++
		counts.perCreatorAct[shard.Creator]++
		return false, nil
	}); err != nil {
		return nil, err
	}

	shards := make(map[uint64]reservedShard)
	if err := k.TrainshardReservations.Walk(ctx, nil, func(key collections.Pair[string, string], id uint64) (bool, error) {
		shard, ok := shards[id]
		if !ok {
			loaded, err := k.Trainshards.Get(ctx, id)
			if err != nil {
				return false, fmt.Errorf("trainshard %d reserving node %s of %s: %w", id, key.K2(), key.K1(), err)
			}
			shard = indexReservedShard(&loaded)
			shards[id] = shard
		}
		models, found := shard.models[nodeKey(key.K1(), key.K2())]
		if !found {
			return false, fmt.Errorf("trainshard %d holds no entry for reserved node %s of %s: %w", id, key.K2(), key.K1(), collections.ErrNotFound)
		}
		counts.total++
		counts.perProfile[shard.profileId]++
		for _, model := range models {
			counts.perModel[model]++
		}
		return false, nil
	}); err != nil {
		return nil, err
	}
	return counts, nil
}

type reservedShard struct {
	profileId string
	models    map[string][]string
}

func indexReservedShard(shard *types.Trainshard) reservedShard {
	indexed := reservedShard{profileId: shard.GpuProfileId, models: make(map[string][]string, len(shard.Nodes))}
	for _, n := range shard.Nodes {
		if n == nil {
			continue
		}
		key := nodeKey(n.Participant, n.NodeId)
		indexed.models[key] = append(indexed.models[key], n.ModelId)
	}
	return indexed
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
	nodeCount uint32,
	params *types.TrainingParams,
) ([]*types.TrainshardReservedNode, uint64, error) {
	view, err := k.buildTrainingEpochView(ctx)
	if err != nil {
		return nil, 0, err
	}
	reserved, err := k.buildTrainingReservedCounts(ctx)
	if err != nil {
		return nil, 0, err
	}
	guardians := k.trainingGuardianSet(ctx)
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()

	var skipped struct {
		nodeId, guardian, challenged, optIn, endpoint, reserved, weightRate, totalCap, profileCap, modelCap int
	}
	ofProfile := 0
	candidates := make([]*trainingEpochNode, 0, len(view.nodes))
	for _, node := range view.nodes {
		if node.profileId == "" || node.profileId != gpuProfileId {
			continue
		}
		ofProfile++
		if types.ValidateTrainingNodeId(node.nodeId) != nil {
			skipped.nodeId++
			continue
		}
		if guardians[node.participant] {
			skipped.guardian++
			continue
		}
		if k.IsUnderChallenge(ctx, node.participant) {
			skipped.challenged++
			continue
		}
		opted, err := k.hasLiveTrainingOptIn(ctx, node.participant, node.nodeId, height)
		if err != nil {
			return nil, 0, err
		}
		if !opted {
			skipped.optIn++
			continue
		}
		// a node no address reaches cannot be driven, so it is not offered: the shard record is
		// the only place a coordinator takes an address from
		node.endpoint, err = k.trainingEndpoint(ctx, node.participant, node.nodeId)
		if err != nil {
			return nil, 0, err
		}
		if node.endpoint == "" {
			skipped.endpoint++
			continue
		}
		if k.IsNodeReserved(ctx, node.participant, node.nodeId) {
			skipped.reserved++
			continue
		}
		if !view.capNodeWeights(node, params) {
			skipped.weightRate++
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
	picked := make([]*types.TrainshardReservedNode, 0, nodeCount)

	for i, node := range candidates {
		if uint32(takenTotal) >= nodeCount {
			break
		}
		if reserved.total+takenTotal+1 > int(params.MaxTotalReservedNodes) {
			skipped.totalCap = len(candidates) - i
			break
		}
		if reserved.perProfile[gpuProfileId]+takenProfile+1 > profileCap {
			skipped.profileCap++
			continue
		}
		// a profile may be emptied: nothing validates per profile, so a profile
		// with no free node left only means no more nodes of that kind
		if view.profileCapacity[gpuProfileId]-(reserved.perProfile[gpuProfileId]+takenProfile+1) < 0 {
			skipped.profileCap++
			continue
		}
		fits := true
		for _, e := range node.entries {
			if reserved.perModel[e.ModelId]+takenModel[e.ModelId]+1 > modelCap(e.ModelId) {
				fits = false
				break
			}
			// a model always keeps one node free, so its PoC weight never reaches
			// zero: chainvalidation accepts every inference of a model whose weight
			// is zero, which would turn a fully reserved model into a free pass
			if view.modelCapacity[e.ModelId]-(reserved.perModel[e.ModelId]+takenModel[e.ModelId]+1) < 1 {
				fits = false
				break
			}
		}
		if !fits {
			skipped.modelCap++
			continue
		}
		for _, e := range node.entries {
			takenModel[e.ModelId]++
			e.Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
			e.Endpoint = node.endpoint
			picked = append(picked, e)
		}
		takenProfile++
		takenTotal++
	}

	if uint32(takenTotal) < nodeCount {
		return nil, 0, types.ErrTrainshardCapacity.Wrapf(
			"found %d of %d needed among %d nodes of profile %s; skipped: %d with an invalid node id, %d guardian, "+
				"%d under a PoC challenge, %d without a live opt-in, "+
				"%d without an endpoint, %d already reserved, %d without a reliable weight rate (%d participants needed), "+
				"%d over the total reserve cap (%d), "+
				"%d over the profile share cap (%d), %d over a model share cap or its last free node",
			takenTotal, nodeCount, ofProfile, gpuProfileId, skipped.nodeId, skipped.guardian, skipped.challenged, skipped.optIn,
			skipped.endpoint, skipped.reserved, skipped.weightRate, params.MinWeightRateParticipants,
			skipped.totalCap, params.MaxTotalReservedNodes,
			skipped.profileCap, profileCap, skipped.modelCap)
	}
	return picked, view.epochIndex, nil
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

// setTrainingEndpoint publishes where the daemon serving the node answers; an empty endpoint
// withdraws it, and a shard assembled after that carries no address for the node
func (k Keeper) setTrainingEndpoint(ctx context.Context, participant, nodeId, endpoint string) error {
	key := collections.Join(participant, nodeId)
	if endpoint == "" {
		return k.TrainingNodeEndpoints.Remove(ctx, key)
	}
	return k.TrainingNodeEndpoints.Set(ctx, key, endpoint)
}

func (k Keeper) trainingEndpoint(ctx context.Context, participant, nodeId string) (string, error) {
	endpoint, err := k.TrainingNodeEndpoints.Get(ctx, collections.Join(participant, nodeId))
	if errors.Is(err, collections.ErrNotFound) {
		return "", nil
	}
	return endpoint, err
}

// clearTrainingOptIn drops everything the host published for the node: the opt-in and the
// endpoint go together, so a node taken out of training leaves no stale address behind
func (k Keeper) clearTrainingOptIn(ctx context.Context, participant, nodeId string) error {
	key := collections.Join(participant, nodeId)
	if err := k.TrainingNodeOptIns.Remove(ctx, key); err != nil {
		return err
	}
	return k.TrainingNodeEndpoints.Remove(ctx, key)
}

func (k Keeper) reserveTrainshardNodes(ctx context.Context, shard *types.Trainshard, weightEpoch uint64) error {
	for _, n := range shard.Nodes {
		if err := k.TrainshardReservations.Set(ctx, collections.Join(n.Participant, n.NodeId), shard.TrainshardId); err != nil {
			return err
		}
	}
	if err := k.Trainshards.Set(ctx, shard.TrainshardId, *shard); err != nil {
		return err
	}
	if err := k.TrainshardWeightEpoch.Set(ctx, shard.TrainshardId, weightEpoch); err != nil {
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

func (k Keeper) CollectReservedNodeIdsAt(ctx context.Context, height int64) map[string]map[string]struct{} {
	reserved := make(map[string]map[string]struct{})
	if err := k.Trainshards.Walk(ctx, nil, func(_ uint64, shard types.Trainshard) (bool, error) {
		for _, n := range shard.Nodes {
			if n == nil {
				continue
			}
			start, end := trainshardNodeWindow(&shard, n, ReservationScopeShield)
			if start > height || end < height {
				continue
			}
			set, ok := reserved[n.Participant]
			if !ok {
				set = make(map[string]struct{})
				reserved[n.Participant] = set
			}
			set[n.NodeId] = struct{}{}
		}
		return false, nil
	}); err != nil {
		k.LogError("CollectReservedNodeIdsAt walk failed", types.Training, "error", err)
	}
	return reserved
}

func modelFreeWeight(p *types.ActiveParticipant, modelId string, reservedWeight int64) (int64, bool) {
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
		weight += node.PocWeight
	}

	return max(0, weight-reservedWeight), true
}

func (k Keeper) collectModelFreeWeights(ctx context.Context, epochId uint64, modelId string) map[string]int64 {
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	byModelHost, byHost := k.BuildEpochReservedWeightView(ctx, epochId, ReservationScopeShield).TotalsAt(height)
	if len(byHost) == 0 {
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
		if _, reserved := byHost[p.Index]; !reserved {
			continue
		}
		if freeWeight, tracked := modelFreeWeight(p, modelId, byModelHost[modelId][p.Index]); tracked {
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

func (k Keeper) FreeShareOfWeightBetween(ctx context.Context, epochIndex uint64, host string, weight, from, to int64) int64 {
	if weight <= 0 {
		return 0
	}
	if to < from {
		return weight
	}
	windows := k.plannedShieldWindows(ctx, host, from, to)
	if len(windows) == 0 {
		return weight
	}
	var epochNodes map[string]int64
	totalRaw := int64(0)
	if active, found := k.GetActiveParticipants(ctx, epochIndex); found {
		for _, p := range active.Participants {
			if p != nil && p.Index == host {
				epochNodes, totalRaw = hostEpochNodeWeights(p), rawPocWeight(p)
			}
		}
	}
	if totalRaw <= 0 {
		return 0
	}
	nodeWeights := make(map[string]int64)
	for _, w := range windows {
		nodeWeights[w.node.NodeId] = max(nodeWeights[w.node.NodeId], w.node.PocWeight)
	}
	reserved := sdkmath.LegacyZeroDec()
	for nodeId, blocks := range leasedBlocks(windows, from, to)[host] {
		reserved = reserved.Add(sdkmath.LegacyNewDec(min(nodeWeights[nodeId], epochNodes[nodeId])).MulInt64(blocks))
	}
	if !reserved.IsPositive() {
		return weight
	}
	capacity := sdkmath.LegacyNewDec(totalRaw).MulInt64(to - from + 1)
	if reserved.GTE(capacity) {
		return 0
	}
	return sdkmath.LegacyNewDec(weight).Mul(capacity.Sub(reserved)).Quo(capacity).TruncateInt64()
}

func (k Keeper) plannedShieldWindows(ctx context.Context, host string, from, to int64) []reservedNodeWindow {
	buffer := k.GetTrainingParams(ctx).ReleaseBufferBlocks
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	var windows []reservedNodeWindow
	if err := k.Trainshards.Walk(ctx, nil, func(_ uint64, shard types.Trainshard) (bool, error) {
		for _, n := range shard.Nodes {
			if n == nil || n.Participant != host {
				continue
			}
			start, end := trainshardNodeWindow(&shard, n, ReservationScopeShield)
			if isActiveReservedNode(n) && shard.ExpiresAtHeight > 0 {
				end = max(shard.ExpiresAtHeight, height) + buffer
			}
			if end < from || start > to {
				continue
			}
			windows = append(windows, reservedNodeWindow{trainshardId: shard.TrainshardId, node: n, start: start, end: end})
		}
		return false, nil
	}); err != nil {
		k.LogError("planned reservation walk failed", types.Training, "error", err)
	}
	return windows
}

func (k Keeper) epochBlockRange(ctx context.Context, epochIndex uint64) (int64, int64) {
	epoch, found := k.GetEpoch(ctx, epochIndex)
	if !found {
		// without epoch metadata the overlap cannot be bounded, so the range is
		// left open: every reservation counts as touching the epoch, which shields
		// hosts rather than penalising them on data we do not have
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

func (k Keeper) forEachEpochReservedNode(ctx context.Context, epochIndex uint64, scope ReservationScope, fn func(trainshardId uint64, n *types.TrainshardReservedNode, start, end int64)) {
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
			fn(shard.TrainshardId, n, start, end)
		}
		return false, nil
	}); err != nil {
		k.LogError("epoch reserved node walk failed", types.Training, "error", err)
	}
}

type EpochReservations struct {
	Nodes        map[string][]*types.TrainshardReservedNode
	LeasedBlocks map[string]map[string]int64
	EpochBlocks  int64
}

func (k Keeper) CollectEpochReservations(ctx context.Context, epochIndex uint64) *EpochReservations {
	windows := k.reservedNodeWindows(ctx, epochIndex, ReservationScopeReward)
	start, end, ok := k.rewardEpochSpan(ctx, epochIndex)
	if !ok {
		return &EpochReservations{Nodes: EpochReservedWeightView{windows: windows}.nodeWeights(nil)}
	}
	windows = windowsWithin(windows, start, end)
	return &EpochReservations{
		Nodes:        EpochReservedWeightView{windows: windows}.nodeWeights(nil),
		LeasedBlocks: leasedBlocks(windows, start, end),
		EpochBlocks:  end - start + 1,
	}
}

func windowsWithin(windows []reservedNodeWindow, start, end int64) []reservedNodeWindow {
	within := make([]reservedNodeWindow, 0, len(windows))
	for _, w := range windows {
		if w.start <= end && w.end >= start {
			within = append(within, w)
		}
	}
	return within
}

func (r *EpochReservations) leasedPart(host, nodeId string, weight int64) int64 {
	if r == nil || r.EpochBlocks <= 0 || weight <= 0 {
		return weight
	}
	leased := min(r.LeasedBlocks[host][nodeId], r.EpochBlocks)
	return sdkmath.LegacyNewDec(weight).MulInt64(leased).QuoInt64(r.EpochBlocks).TruncateInt64()
}

func (k Keeper) rewardEpochSpan(ctx context.Context, epochIndex uint64) (int64, int64, bool) {
	epoch, found := k.GetEpoch(ctx, epochIndex)
	if !found {
		return 0, 0, false
	}
	start := epoch.PocStartBlockHeight
	if next, ok := k.GetEpoch(ctx, epochIndex+1); ok && next.PocStartBlockHeight > start {
		return start, next.PocStartBlockHeight - 1, true
	}
	params, err := k.GetParams(ctx)
	if err != nil || params.EpochParams == nil || params.EpochParams.EpochLength <= 0 {
		return 0, 0, false
	}
	return start, start + params.EpochParams.EpochLength - 1, true
}

func leasedBlocks(windows []reservedNodeWindow, start, end int64) map[string]map[string]int64 {
	type interval struct{ start, end int64 }
	intervals := make(map[string]map[string][]interval)
	for _, w := range windows {
		from, to := max(w.start, start), min(w.end, end)
		if from > to {
			continue
		}
		host := w.node.Participant
		if intervals[host] == nil {
			intervals[host] = make(map[string][]interval)
		}
		intervals[host][w.node.NodeId] = append(intervals[host][w.node.NodeId], interval{from, to})
	}
	leased := make(map[string]map[string]int64, len(intervals))
	for host, nodes := range intervals {
		leased[host] = make(map[string]int64, len(nodes))
		for nodeId, list := range nodes {
			sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })
			total := int64(0)
			from, to := list[0].start, list[0].end
			for _, iv := range list[1:] {
				if iv.start > to {
					total += to - from + 1
					from, to = iv.start, iv.end
					continue
				}
				to = max(to, iv.end)
			}
			leased[host][nodeId] = total + to - from + 1
		}
	}
	return leased
}

func (k Keeper) CollectEpochReservedNodeWeights(ctx context.Context, epochIndex uint64, scope ReservationScope) map[string][]*types.TrainshardReservedNode {
	return k.BuildEpochReservedWeightView(ctx, epochIndex, scope).nodeWeights(nil)
}

func (k Keeper) CollectEpochReservedNodeWeightsAtHeight(ctx context.Context, epochIndex uint64, height int64, scope ReservationScope) map[string][]*types.TrainshardReservedNode {
	return k.BuildEpochReservedWeightView(ctx, epochIndex, scope).nodeWeights(&height)
}

// CollectEpochReservedNodeWeightsBetween returns the nodes reserved at any height in [from, to]
func (k Keeper) CollectEpochReservedNodeWeightsBetween(ctx context.Context, epochIndex uint64, from, to int64, scope ReservationScope) map[string][]*types.TrainshardReservedNode {
	return k.BuildEpochReservedWeightView(ctx, epochIndex, scope).nodeWeightsBetween(from, to)
}

func (k Keeper) CollectCarriedReservedNodeWeights(ctx context.Context, upcomingEpoch types.Epoch) map[string][]*types.TrainshardReservedNode {
	if upcomingEpoch.Index == 0 {
		return nil
	}
	view := EpochReservedWeightView{windows: k.reservedNodeWindows(ctx, upcomingEpoch.Index, ReservationScopeShield)}
	height := upcomingEpoch.PocStartBlockHeight
	return view.nodeWeights(&height)
}

func (k Keeper) CarriedReservationWeightEpochs(ctx context.Context, upcomingEpoch types.Epoch) map[string]uint64 {
	if upcomingEpoch.Index == 0 {
		return nil
	}
	height := upcomingEpoch.PocStartBlockHeight
	epochs := make(map[string]uint64)
	unanchored := make(map[string]bool)
	for _, w := range k.reservedNodeWindows(ctx, upcomingEpoch.Index, ReservationScopeShield) {
		if w.start > height || w.end < height {
			continue
		}
		host := w.node.Participant
		epoch, err := k.TrainshardWeightEpoch.Get(ctx, w.trainshardId)
		if err != nil {
			k.LogError("carried trainshard has no weight epoch, its host carries nothing", types.Training,
				"trainshard_id", w.trainshardId, "participant", host, "error", err)
			unanchored[host] = true
			continue
		}
		if prev, seen := epochs[host]; !seen || epoch < prev {
			epochs[host] = epoch
		}
	}
	for host := range unanchored {
		delete(epochs, host)
	}
	return epochs
}

type EpochReservedWeightView struct {
	windows      []reservedNodeWindow
	clamped      bool
	epochWeights map[string]map[string]int64
}

type reservedNodeWindow struct {
	trainshardId uint64
	node         *types.TrainshardReservedNode
	start        int64
	end          int64
}

func (k Keeper) BuildEpochReservedWeightView(ctx context.Context, epochIndex uint64, scope ReservationScope) EpochReservedWeightView {
	view := EpochReservedWeightView{windows: k.reservedNodeWindows(ctx, epochIndex, scope)}
	view.clampTo(k.GetActiveParticipants(ctx, epochIndex))
	return view
}

func (k Keeper) reservedNodeWindows(ctx context.Context, epochIndex uint64, scope ReservationScope) []reservedNodeWindow {
	var windows []reservedNodeWindow
	k.forEachEpochReservedNode(ctx, epochIndex, scope, func(trainshardId uint64, n *types.TrainshardReservedNode, start, end int64) {
		windows = append(windows, reservedNodeWindow{trainshardId: trainshardId, node: n, start: start, end: end})
	})
	return windows
}

func (v *EpochReservedWeightView) clampTo(active types.ActiveParticipants, found bool) {
	if !found {
		return
	}
	v.clamped = true
	v.epochWeights = epochNodeWeights(&active)
}

func (v EpochReservedWeightView) clamp(host, nodeId string, weight int64) int64 {
	if !v.clamped {
		return weight
	}
	return min(weight, v.epochWeights[host][nodeId])
}

func epochNodeWeights(active *types.ActiveParticipants) map[string]map[string]int64 {
	weights := make(map[string]map[string]int64, len(active.Participants))
	for _, p := range active.Participants {
		if p != nil && p.Index != "" {
			weights[p.Index] = hostEpochNodeWeights(p)
		}
	}
	return weights
}

func (v EpochReservedWeightView) TotalsAt(height int64) (byModelHost map[string]map[string]int64, byHost map[string]int64) {
	return aggregateReservedWeightTotals(v.nodeWeights(&height))
}

func (v EpochReservedWeightView) nodeWeights(height *int64) map[string][]*types.TrainshardReservedNode {
	if height == nil {
		return v.nodeWeightsBetween(math.MinInt64, math.MaxInt64)
	}
	return v.nodeWeightsBetween(*height, *height)
}

func (v EpochReservedWeightView) nodeWeightsBetween(from, to int64) map[string][]*types.TrainshardReservedNode {
	type modelNode struct{ model, nodeId string }
	seen := make(map[string]map[modelNode]int64)
	for _, w := range v.windows {
		n := w.node
		if w.end < from || w.start > to {
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
				PocWeight:   v.clamp(participant, mn.nodeId, weight),
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
	weight int64
}

func (k Keeper) collectEpochReservedIntervals(ctx context.Context, epochIndex uint64) map[string][]hostReservedInterval {
	result := make(map[string][]hostReservedInterval)
	seen := make(map[string]map[hostReservedInterval]bool)
	k.forEachEpochReservedNode(ctx, epochIndex, ReservationScopeShield, func(_ uint64, n *types.TrainshardReservedNode, start, end int64) {
		interval := hostReservedInterval{start: start, end: end, nodeId: n.NodeId, weight: n.PocWeight}
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

func hostFullyReservedAtHeight(epochNodes map[string]int64, intervals []hostReservedInterval, height int64) bool {
	if len(epochNodes) == 0 || len(intervals) == 0 {
		return false
	}
	reserved := make(map[string]int64, len(epochNodes))
	for _, iv := range intervals {
		if iv.start > height || iv.end < height {
			continue
		}
		if _, want := epochNodes[iv.nodeId]; !want {
			continue
		}
		if prev, covered := reserved[iv.nodeId]; !covered || iv.weight > prev {
			reserved[iv.nodeId] = iv.weight
		}
	}
	for nodeId, weight := range epochNodes {
		reservedWeight, covered := reserved[nodeId]
		if !covered || reservedWeight < weight {
			return false
		}
	}
	return true
}

func hostEpochNodeWeights(p *types.ActiveParticipant) map[string]int64 {
	nodes := make(map[string]int64)
	for i := range p.Models {
		if i >= len(p.MlNodes) || p.MlNodes[i] == nil {
			continue
		}
		for _, n := range p.MlNodes[i].MlNodes {
			if n == nil || n.NodeId == "" {
				continue
			}
			if prev, seen := nodes[n.NodeId]; !seen || n.PocWeight > prev {
				nodes[n.NodeId] = n.PocWeight
			}
		}
	}
	return nodes
}

type EpochReservationView struct {
	intervals map[string][]hostReservedInterval
	nodes     map[string]map[string]int64
}

func (k Keeper) BuildEpochReservationView(ctx context.Context, epochIndex uint64) EpochReservationView {
	v := EpochReservationView{
		intervals: k.collectEpochReservedIntervals(ctx, epochIndex),
		nodes:     make(map[string]map[string]int64),
	}
	if active, found := k.GetActiveParticipants(ctx, epochIndex); found {
		v.nodes = epochNodeWeights(&active)
	}
	return v
}

func (v EpochReservationView) FullyReservedAt(host string, height int64) bool {
	return hostFullyReservedAtHeight(v.nodes[host], v.intervals[host], height)
}

func (k Keeper) CollectEpochReservedHostsForModel(ctx context.Context, epochIndex uint64, modelId string, scope ReservationScope) map[string]struct{} {
	hosts := make(map[string]struct{})
	for host, nodes := range k.BuildEpochReservedWeightView(ctx, epochIndex, scope).nodeWeights(nil) {
		if host == "" {
			continue
		}
		for _, n := range nodes {
			if n.ModelId == modelId && n.PocWeight > 0 {
				hosts[host] = struct{}{}
				break
			}
		}
	}
	return hosts
}
