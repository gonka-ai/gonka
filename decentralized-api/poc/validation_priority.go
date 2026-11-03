package poc

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math/bits"
	"math/rand"
	"slices"

	"github.com/productscience/inference/x/inference/types"
)

// Local ordering for an already-built eligible PoC validation list.
//
// ValidateAll dispatches orderOrdinaryValidationWork. Challenge validation
// shuffles its own list and does not call the planner. executeValidation
// keeps the order it is given.

const (
	priorityImpactBurst    = 8
	priorityCoverageBurst  = 1
	priorityAdmissionBurst = 1

	priorityDomainImpact    = "gonka/poc-validation-order/v2/impact"
	priorityDomainCoverage  = "gonka/poc-validation-order/v2/coverage"
	priorityDomainAdmission = "gonka/poc-validation-order/v2/admission"
	priorityDomainRotation  = "gonka/poc-validation-order/v2/coverage-rotation"
)

type priorityFallbackReason string

const (
	fallbackSnapshotMissing  priorityFallbackReason = "snapshot_missing"
	fallbackSnapshotPartial  priorityFallbackReason = "snapshot_partial"
	fallbackSnapshotInvalid  priorityFallbackReason = "snapshot_invalid"
	fallbackValidatorMissing priorityFallbackReason = "validator_missing"
)

type prioritySnapshot struct {
	stage            int64
	appHash          []byte
	modelVotingPower map[string]map[string]int64
	validator        []byte
	// Guardian promotion stays off until stage data freezes the enabled flag
	// and the canonical guardian account set. An address list alone is not enough.
	guardianEnabled bool
	guardians       map[string]struct{}
}

type validationLane string

const (
	laneGuardian  validationLane = "guardian"
	laneImpact    validationLane = "impact"
	laneCoverage  validationLane = "coverage"
	laneAdmission validationLane = "admission"
)

type validationOrderDecision struct {
	Mode          string
	Reason        string
	Guardian      int
	Established   int
	Admission     int
	LaneGuardian  int
	LaneImpact    int
	LaneCoverage  int
	LaneAdmission int
}

type validationOrderItem struct {
	index        int
	guardian     bool
	powerBand    uint8
	impactKey    [sha256.Size]byte
	coverageKey  [sha256.Size]byte
	admissionKey [sha256.Size]byte
}

// authoritativePrioritySnapshot accepts a snapshot only when every scheduling
// input is complete. CanonicalAccAddress uses the process bech32 prefix; a
// mismatch fails authority. Guardian promotion is left disabled.
func authoritativePrioritySnapshot(
	stage int64,
	validatorAddress string,
	resp *types.QueryPoCValidationSnapshotResponse,
) (*prioritySnapshot, priorityFallbackReason) {
	if resp == nil || !resp.Found || resp.Snapshot == nil {
		return nil, fallbackSnapshotMissing
	}
	if validatorAddress == "" {
		return nil, fallbackValidatorMissing
	}
	validator, err := types.CanonicalAccAddress(validatorAddress)
	if err != nil {
		return nil, fallbackValidatorMissing
	}

	stored := resp.Snapshot
	if stored.PocStageStartHeight != stage || stored.SnapshotHeight <= 0 || stored.AppHash == "" {
		return nil, fallbackSnapshotPartial
	}
	appHash, err := hex.DecodeString(stored.AppHash)
	if err != nil || len(appHash) != sha256.Size {
		return nil, fallbackSnapshotInvalid
	}
	powers, reason := positiveModelVotingPowers(stored.ModelVotingPowers)
	if reason != "" {
		return nil, reason
	}

	hashCopy := make([]byte, sha256.Size)
	copy(hashCopy, appHash)
	validatorCopy := make([]byte, len(validator))
	copy(validatorCopy, validator)
	return &prioritySnapshot{
		stage:            stage,
		appHash:          hashCopy,
		modelVotingPower: powers,
		validator:        validatorCopy,
	}, ""
}

func positiveModelVotingPowers(models []*types.ModelVotingPowers) (map[string]map[string]int64, priorityFallbackReason) {
	if len(models) == 0 {
		return nil, fallbackSnapshotPartial
	}
	out := make(map[string]map[string]int64)
	for _, model := range models {
		if model == nil || model.ModelId == "" {
			return nil, fallbackSnapshotInvalid
		}
		if _, exists := out[model.ModelId]; exists {
			return nil, fallbackSnapshotInvalid
		}
		seen := make(map[string]struct{}, len(model.VotingPowers))
		var positive map[string]int64
		for _, entry := range model.VotingPowers {
			if entry == nil || entry.Address == "" {
				return nil, fallbackSnapshotInvalid
			}
			if _, err := types.CanonicalAccAddress(entry.Address); err != nil {
				return nil, fallbackSnapshotInvalid
			}
			if _, dup := seen[entry.Address]; dup {
				return nil, fallbackSnapshotInvalid
			}
			seen[entry.Address] = struct{}{}
			if entry.VotingPower <= 0 {
				continue
			}
			if positive == nil {
				positive = make(map[string]int64)
			}
			positive[entry.Address] = entry.VotingPower
		}
		if len(positive) > 0 {
			out[model.ModelId] = positive
		}
	}
	if len(out) == 0 {
		return nil, fallbackSnapshotPartial
	}
	return out, ""
}

// orderOrdinaryValidationWork returns the queue ValidateAll dispatches.
// An incomplete snapshot shuffles the complete eligible list. rng nil uses
// the global math/rand source, matching the previous executeValidation shuffle.
func orderOrdinaryValidationWork(
	stage int64,
	validatorAddress string,
	resp *types.QueryPoCValidationSnapshotResponse,
	items []participantWork,
	workerCount int,
	rng *rand.Rand,
) ([]participantWork, validationOrderDecision) {
	snap, reason := authoritativePrioritySnapshot(stage, validatorAddress, resp)
	if reason != "" || snap == nil {
		shuffleValidationWork(items, rng)
		return items, validationOrderDecision{Mode: "shuffle", Reason: string(reason)}
	}
	meta := deriveValidationOrder(snap, items)
	ordered, lanes := prioritizeValidationWork(items, snap, meta, workerCount)
	if len(ordered) != len(items) || len(lanes) != len(items) {
		shuffleValidationWork(items, rng)
		return items, validationOrderDecision{Mode: "shuffle", Reason: "permutation"}
	}
	return ordered, summarizeValidationOrder(meta, lanes)
}

func summarizeValidationOrder(meta []validationOrderItem, lanes []validationLane) validationOrderDecision {
	decision := validationOrderDecision{Mode: "priority"}
	for _, item := range meta {
		switch {
		case item.guardian:
			decision.Guardian++
		case item.powerBand > 0:
			decision.Established++
		default:
			decision.Admission++
		}
	}
	for _, lane := range lanes {
		switch lane {
		case laneGuardian:
			decision.LaneGuardian++
		case laneImpact:
			decision.LaneImpact++
		case laneCoverage:
			decision.LaneCoverage++
		case laneAdmission:
			decision.LaneAdmission++
		}
	}
	return decision
}

func shuffleValidationWork(items []participantWork, rng *rand.Rand) {
	if len(items) < 2 {
		return
	}
	swap := func(i, j int) {
		items[i], items[j] = items[j], items[i]
	}
	if rng == nil {
		rand.Shuffle(len(items), swap)
		return
	}
	rng.Shuffle(len(items), swap)
}

func deriveValidationOrder(snap *prioritySnapshot, items []participantWork) []validationOrderItem {
	meta := make([]validationOrderItem, len(items))
	for i, item := range items {
		meta[i].index = i
		if snap == nil {
			continue
		}
		target := schedulingAccountBytes(item.address)
		meta[i].impactKey = validationOrderKey(priorityDomainImpact, snap, target, item.modelId)
		meta[i].coverageKey = validationOrderKey(priorityDomainCoverage, snap, target, item.modelId)
		meta[i].admissionKey = validationOrderKey(priorityDomainAdmission, snap, target, item.modelId)
		meta[i].guardian = isGuardianTarget(snap, item.address)
		meta[i].powerBand = powerBand(modelPower(snap, item.modelId, item.address))
	}
	return meta
}

func schedulingAccountBytes(address string) []byte {
	addr, err := types.CanonicalAccAddress(address)
	if err != nil {
		encoded := make([]byte, 1+8+len(address))
		encoded[0] = 0xff
		binary.BigEndian.PutUint64(encoded[1:9], uint64(len(address)))
		copy(encoded[9:], address)
		return encoded
	}
	out := make([]byte, len(addr))
	copy(out, addr)
	return out
}

func validationOrderKey(domain string, snap *prioritySnapshot, target []byte, modelID string) [sha256.Size]byte {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(domain))
	var num [8]byte
	var stage int64
	var appHash, validator []byte
	if snap != nil {
		stage = snap.stage
		appHash = snap.appHash
		validator = snap.validator
	}
	binary.BigEndian.PutUint64(num[:], uint64(stage))
	_, _ = hasher.Write(num[:])
	_, _ = hasher.Write(appHash)
	_, _ = hasher.Write(validator)
	_, _ = hasher.Write(target)
	binary.BigEndian.PutUint64(num[:], uint64(len(modelID)))
	_, _ = hasher.Write(num[:])
	_, _ = hasher.Write([]byte(modelID))
	var out [sha256.Size]byte
	copy(out[:], hasher.Sum(nil))
	return out
}

func isGuardianTarget(snap *prioritySnapshot, address string) bool {
	if snap == nil || !snap.guardianEnabled || len(snap.guardians) == 0 {
		return false
	}
	addr, err := types.CanonicalAccAddress(address)
	if err != nil {
		return false
	}
	_, ok := snap.guardians[addr.String()]
	return ok
}

func modelPower(snap *prioritySnapshot, modelID, address string) int64 {
	if snap == nil {
		return 0
	}
	power := snap.modelVotingPower[modelID][address]
	if power <= 0 {
		return 0
	}
	return power
}

func powerBand(power int64) uint8 {
	if power <= 0 {
		return 0
	}
	return uint8(bits.Len64(uint64(power)))
}

// prioritizeValidationWork returns a permutation of items. Lane labels describe
// the initial-attempt cycle only. Guardian prefix length is one worker wave.
func prioritizeValidationWork(
	items []participantWork,
	snap *prioritySnapshot,
	meta []validationOrderItem,
	validationWorkerCount int,
) ([]participantWork, []validationLane) {
	if len(items) == 0 {
		return nil, nil
	}
	if validationWorkerCount < 0 {
		validationWorkerCount = 0
	}

	guardians := guardianItems(meta)
	impact := establishedByBand(meta)
	coverage := establishedByCoverage(meta, snap)
	admission := admissionItems(meta)

	emitted := make([]bool, len(items))
	ordered := make([]participantWork, 0, len(items))
	lanes := make([]validationLane, 0, len(items))
	appendOnce := func(item validationOrderItem, lane validationLane) bool {
		if item.index < 0 || item.index >= len(items) || emitted[item.index] {
			return false
		}
		emitted[item.index] = true
		ordered = append(ordered, items[item.index])
		lanes = append(lanes, lane)
		return true
	}

	prefix := validationWorkerCount
	if prefix > len(guardians) {
		prefix = len(guardians)
	}
	guardianPos := 0
	takeUnemitted(guardians, &guardianPos, prefix, laneGuardian, appendOnce)

	var impactPos, coveragePos, admissionPos int
	for len(ordered) < len(items) {
		before := len(ordered)
		takeUnemitted(impact, &impactPos, priorityImpactBurst, laneImpact, appendOnce)
		takeUnemitted(coverage, &coveragePos, priorityCoverageBurst, laneCoverage, appendOnce)
		takeUnemitted(admission, &admissionPos, priorityAdmissionBurst, laneAdmission, appendOnce)
		if len(ordered) == before {
			for i := range items {
				appendOnce(validationOrderItem{index: i}, laneForIndex(meta, i))
			}
			break
		}
	}
	return ordered, lanes
}

func takeUnemitted(
	items []validationOrderItem,
	pos *int,
	n int,
	lane validationLane,
	appendOnce func(validationOrderItem, validationLane) bool,
) {
	if n <= 0 {
		return
	}
	taken := 0
	for taken < n && *pos < len(items) {
		if appendOnce(items[*pos], lane) {
			taken++
		}
		*pos++
	}
}

func laneForIndex(meta []validationOrderItem, index int) validationLane {
	for _, item := range meta {
		if item.index == index && item.powerBand > 0 {
			return laneImpact
		}
	}
	return laneAdmission
}

func guardianItems(meta []validationOrderItem) []validationOrderItem {
	return filterSort(meta, func(item validationOrderItem) bool {
		return item.guardian
	}, func(a, b validationOrderItem) int {
		return compareKeyIndex(a.impactKey, b.impactKey, a.index, b.index)
	})
}

func establishedByBand(meta []validationOrderItem) []validationOrderItem {
	return filterSort(meta, func(item validationOrderItem) bool {
		return item.powerBand > 0
	}, func(a, b validationOrderItem) int {
		if a.powerBand != b.powerBand {
			return cmp.Compare(b.powerBand, a.powerBand)
		}
		return compareKeyIndex(a.impactKey, b.impactKey, a.index, b.index)
	})
}

func admissionItems(meta []validationOrderItem) []validationOrderItem {
	return filterSort(meta, func(item validationOrderItem) bool {
		return item.powerBand == 0
	}, func(a, b validationOrderItem) int {
		return compareKeyIndex(a.admissionKey, b.admissionKey, a.index, b.index)
	})
}

func establishedByCoverage(meta []validationOrderItem, snap *prioritySnapshot) []validationOrderItem {
	groups := make(map[uint8][]validationOrderItem)
	var bands []uint8
	for _, item := range meta {
		if item.powerBand == 0 {
			continue
		}
		if _, ok := groups[item.powerBand]; !ok {
			bands = append(bands, item.powerBand)
		}
		groups[item.powerBand] = append(groups[item.powerBand], item)
	}
	slices.Sort(bands)
	for band, grouped := range groups {
		slices.SortFunc(grouped, func(a, b validationOrderItem) int {
			return compareKeyIndex(a.coverageKey, b.coverageKey, a.index, b.index)
		})
		groups[band] = grouped
	}
	offset := coverageRotationOffset(snap, len(bands))
	order := make([]uint8, len(bands))
	for i := range bands {
		order[i] = bands[(i+offset)%len(bands)]
	}
	return roundRobinByBand(order, groups)
}

func roundRobinByBand(order []uint8, groups map[uint8][]validationOrderItem) []validationOrderItem {
	total := 0
	for _, band := range order {
		total += len(groups[band])
	}
	if total == 0 {
		return nil
	}
	cursors := make(map[uint8]int, len(order))
	out := make([]validationOrderItem, 0, total)
	for len(out) < total {
		progressed := false
		for _, band := range order {
			grouped := groups[band]
			cursor := cursors[band]
			if cursor >= len(grouped) {
				continue
			}
			out = append(out, grouped[cursor])
			cursors[band] = cursor + 1
			progressed = true
		}
		if !progressed {
			break
		}
	}
	return out
}

func coverageRotationOffset(snap *prioritySnapshot, n int) int {
	if snap == nil || n <= 1 {
		return 0
	}
	sum := validationOrderKey(priorityDomainRotation, snap, nil, "")
	value := binary.BigEndian.Uint64(sum[:8])
	return int(value % uint64(n))
}

func filterSort(
	meta []validationOrderItem,
	keep func(validationOrderItem) bool,
	less func(a, b validationOrderItem) int,
) []validationOrderItem {
	out := make([]validationOrderItem, 0)
	for _, item := range meta {
		if keep(item) {
			out = append(out, item)
		}
	}
	slices.SortFunc(out, less)
	return out
}

func compareKeyIndex(aKey, bKey [sha256.Size]byte, aIndex, bIndex int) int {
	if compared := slices.Compare(aKey[:], bKey[:]); compared != 0 {
		return compared
	}
	return cmp.Compare(aIndex, bIndex)
}
