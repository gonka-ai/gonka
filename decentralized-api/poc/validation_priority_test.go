package poc

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const testAppHashHex = "1111111111111111111111111111111111111111111111111111111111111111"

func accAddrN(n int) string {
	var raw [20]byte
	raw[0] = 0x02
	binary.BigEndian.PutUint32(raw[16:], uint32(n))
	return sdk.AccAddress(raw[:]).String()
}

func workItem(address, model string, count uint32) participantWork {
	return participantWork{address: address, modelId: model, count: count}
}

func generationStartStub(stage int64) *types.QueryPoCValidationSnapshotResponse {
	return &types.QueryPoCValidationSnapshotResponse{
		Found: true,
		Snapshot: &types.PoCValidationSnapshot{
			PocStageStartHeight:      stage,
			GenerationStartTimestamp: 1_700_000_000,
		},
	}
}

func authorityFixture(stage int64, validator string, powers []*types.VotingPowerEntry) *types.QueryPoCValidationSnapshotResponse {
	return &types.QueryPoCValidationSnapshotResponse{
		Found: true,
		Snapshot: &types.PoCValidationSnapshot{
			PocStageStartHeight: stage,
			SnapshotHeight:      stage + 5,
			AppHash:             testAppHashHex,
			ModelVotingPowers: []*types.ModelVotingPowers{{
				ModelId:      "model-a",
				VotingPowers: powers,
			}},
		},
	}
}

func TestAuthoritativeSnapshotFallback(t *testing.T) {
	stage := int64(100)
	validator := accAddrN(1)
	positive := []*types.VotingPowerEntry{{Address: accAddrN(2), VotingPower: 8}}
	valid := authorityFixture(stage, validator, positive)

	uppercase := strings.ToUpper(validator)
	shortHash := hex.EncodeToString(bytes.Repeat([]byte{0x11}, 16))
	longHash := hex.EncodeToString(bytes.Repeat([]byte{0x11}, 33))

	tests := []struct {
		name      string
		stage     int64
		validator string
		resp      *types.QueryPoCValidationSnapshotResponse
		reason    priorityFallbackReason
	}{
		{name: "nil response", stage: stage, validator: validator, reason: fallbackSnapshotMissing},
		{name: "not found", stage: stage, validator: validator, resp: &types.QueryPoCValidationSnapshotResponse{Found: false, Snapshot: valid.Snapshot}, reason: fallbackSnapshotMissing},
		{name: "nil snapshot", stage: stage, validator: validator, resp: &types.QueryPoCValidationSnapshotResponse{Found: true}, reason: fallbackSnapshotMissing},
		{name: "generation-start stub", stage: stage, validator: validator, resp: generationStartStub(stage), reason: fallbackSnapshotPartial},
		{name: "empty app hash", stage: stage, validator: validator, resp: withAppHash(valid, ""), reason: fallbackSnapshotPartial},
		{name: "zero snapshot height", stage: stage, validator: validator, resp: withHeight(valid, 0), reason: fallbackSnapshotPartial},
		{name: "stage mismatch", stage: stage, validator: validator, resp: valid, reason: fallbackSnapshotPartial},
		{name: "empty powers", stage: stage, validator: validator, resp: authorityFixture(stage, validator, nil), reason: fallbackSnapshotPartial},
		{name: "only non-positive powers", stage: stage, validator: validator, resp: authorityFixture(stage, validator, []*types.VotingPowerEntry{{Address: accAddrN(2), VotingPower: 0}, {Address: accAddrN(3), VotingPower: -5}}), reason: fallbackSnapshotPartial},
		{name: "malformed app hash", stage: stage, validator: validator, resp: withAppHash(valid, "zz"), reason: fallbackSnapshotInvalid},
		{name: "short app hash", stage: stage, validator: validator, resp: withAppHash(valid, shortHash), reason: fallbackSnapshotInvalid},
		{name: "long app hash", stage: stage, validator: validator, resp: withAppHash(valid, longHash), reason: fallbackSnapshotInvalid},
		{name: "nil model", stage: stage, validator: validator, resp: withModels(valid, []*types.ModelVotingPowers{nil}), reason: fallbackSnapshotInvalid},
		{name: "empty model id", stage: stage, validator: validator, resp: withModels(valid, []*types.ModelVotingPowers{{ModelId: "", VotingPowers: positive}}), reason: fallbackSnapshotInvalid},
		{name: "duplicate model", stage: stage, validator: validator, resp: withModels(valid, []*types.ModelVotingPowers{valid.Snapshot.ModelVotingPowers[0], valid.Snapshot.ModelVotingPowers[0]}), reason: fallbackSnapshotInvalid},
		{name: "nil power entry", stage: stage, validator: validator, resp: authorityFixture(stage, validator, []*types.VotingPowerEntry{nil}), reason: fallbackSnapshotInvalid},
		{name: "duplicate address", stage: stage, validator: validator, resp: authorityFixture(stage, validator, []*types.VotingPowerEntry{positive[0], positive[0]}), reason: fallbackSnapshotInvalid},
		{name: "non-canonical power address", stage: stage, validator: validator, resp: authorityFixture(stage, validator, []*types.VotingPowerEntry{{Address: strings.ToUpper(accAddrN(2)), VotingPower: 8}}), reason: fallbackSnapshotInvalid},
		{name: "empty validator", stage: stage, validator: "", resp: valid, reason: fallbackValidatorMissing},
		{name: "non-canonical validator", stage: stage, validator: uppercase, resp: valid, reason: fallbackValidatorMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestStage := stage
			if tt.name == "stage mismatch" {
				requestStage = stage + 1
			}
			before := append([]participantWork(nil), sampleWork(validator)...)
			snap, reason := authoritativePrioritySnapshot(requestStage, tt.validator, tt.resp)
			if reason != tt.reason {
				t.Fatalf("reason = %q, want %q", reason, tt.reason)
			}
			if snap != nil {
				t.Fatal("fallback returned a snapshot")
			}
			if !sameWorkOrder(before, sampleWork(validator)) {
				t.Fatal("fixture helper changed")
			}
		})
	}
}

func TestAuthoritativeSnapshotAcceptsCompleteSnapshot(t *testing.T) {
	stage := int64(100)
	validator := accAddrN(1)
	resp := authorityFixture(stage, validator, []*types.VotingPowerEntry{
		{Address: accAddrN(2), VotingPower: 8},
		{Address: accAddrN(3), VotingPower: 0},
		{Address: accAddrN(4), VotingPower: -1},
	})
	resp.Snapshot.ModelVotingPowers = append(resp.Snapshot.ModelVotingPowers, &types.ModelVotingPowers{
		ModelId: "model-b",
		VotingPowers: []*types.VotingPowerEntry{
			{Address: accAddrN(5), VotingPower: -2},
		},
	})
	resp.Snapshot.AppHash = strings.ToUpper(testAppHashHex)

	snap, reason := authoritativePrioritySnapshot(stage, validator, resp)
	if reason != "" || snap == nil {
		t.Fatalf("accepted snapshot reason=%q snap=%v", reason, snap)
	}
	if snap.guardianEnabled || len(snap.guardians) != 0 {
		t.Fatal("chain snapshot enabled guardian promotion")
	}
	if snap.modelVotingPower["model-a"][accAddrN(2)] != 8 {
		t.Fatalf("positive power = %d", snap.modelVotingPower["model-a"][accAddrN(2)])
	}
	if _, ok := snap.modelVotingPower["model-a"][accAddrN(3)]; ok {
		t.Fatal("zero power stored as established")
	}
	if _, ok := snap.modelVotingPower["model-a"][accAddrN(4)]; ok {
		t.Fatal("negative power stored as established")
	}
	if _, ok := snap.modelVotingPower["model-b"]; ok {
		t.Fatal("model with only negative power became established")
	}
	if snap.stage != stage || len(snap.appHash) != 32 {
		t.Fatalf("stage %d hash len %d", snap.stage, len(snap.appHash))
	}
}

func TestFallbackDoesNotReorderEligibleWork(t *testing.T) {
	items := []participantWork{
		workItem(accAddrN(2), "model-a", 1),
		workItem(accAddrN(3), "model-a", 2),
		workItem("not-an-address", "model-a", 3),
	}
	before := append([]participantWork(nil), items...)
	snap, reason := authoritativePrioritySnapshot(100, accAddrN(1), generationStartStub(100))
	if reason != fallbackSnapshotPartial || snap != nil {
		t.Fatalf("reason=%q snap=%v", reason, snap)
	}
	if !sameWorkOrder(items, before) {
		t.Fatal("authority check reordered eligible work")
	}
}

func TestPowerBandMapping(t *testing.T) {
	tests := []struct {
		power int64
		band  uint8
	}{
		{power: math.MinInt64, band: 0},
		{power: -1, band: 0},
		{power: 0, band: 0},
		{power: 1, band: 1},
		{power: 2, band: 2},
		{power: 3, band: 2},
		{power: 4, band: 3},
		{power: 7, band: 3},
		{power: 512, band: 10},
		{power: 1023, band: 10},
		{power: 1024, band: 11},
		{power: 2047, band: 11},
		{power: math.MaxInt64, band: 63},
	}
	for _, tt := range tests {
		if got := powerBand(tt.power); got != tt.band {
			t.Fatalf("power %d band = %d, want %d", tt.power, got, tt.band)
		}
	}
}

func TestPrioritizeIsPermutationAndLeavesInput(t *testing.T) {
	items, snap := mixedWork(t)
	before := append([]participantWork(nil), items...)
	ordered, lanes := plan(items, snap, 4)
	assertPermutation(t, items, ordered)
	if len(lanes) != len(ordered) {
		t.Fatalf("lanes %d ordered %d", len(lanes), len(ordered))
	}
	if !sameWorkOrder(items, before) {
		t.Fatal("planner reordered the input slice")
	}
}

func TestChainSnapshotDoesNotPromoteGuardians(t *testing.T) {
	stage := int64(100)
	guardian := accAddrN(7)
	validator := accAddrN(1)
	resp := authorityFixture(stage, validator, []*types.VotingPowerEntry{{Address: guardian, VotingPower: 4096}})
	snap, reason := authoritativePrioritySnapshot(stage, validator, resp)
	if reason != "" {
		t.Fatal(reason)
	}
	items := []participantWork{workItem(guardian, "model-a", 1), workItem(accAddrN(8), "model-a", 2)}
	_, lanes := plan(items, snap, 4)
	for i, lane := range lanes {
		if lane == laneGuardian {
			t.Fatalf("position %d used the guardian prefix", i)
		}
	}
}

func TestGuardianPrefixIsOneWorkerWave(t *testing.T) {
	validator := accAddrN(1)
	snap := manualSnap(validator, nil)
	snap.guardianEnabled = true
	snap.guardians = map[string]struct{}{}
	var items []participantWork
	for i := 0; i < 5; i++ {
		addr := accAddrN(10 + i)
		snap.guardians[addr] = struct{}{}
		items = append(items, workItem(addr, "model-a", uint32(i)))
		snap.modelVotingPower["model-a"][addr] = 1024
	}
	items = append(items, workItem(accAddrN(30), "model-a", 100))
	snap.modelVotingPower["model-a"][accAddrN(30)] = 2

	ordered, lanes := plan(items, snap, 2)
	assertPermutation(t, items, ordered)
	if countLane(lanes, laneGuardian) != 2 {
		t.Fatalf("guardian prefix = %d, want 2", countLane(lanes, laneGuardian))
	}
	for _, lane := range lanes[2:] {
		if lane == laneGuardian {
			t.Fatal("guardian prefix continued past one worker wave")
		}
	}
	guardianLater := 0
	for i, item := range ordered[2:] {
		if _, ok := snap.guardians[item.address]; ok {
			guardianLater++
			if lanes[i+2] == laneGuardian {
				t.Fatal("excess guardian stayed in the prefix lane")
			}
		}
	}
	if guardianLater != 3 {
		t.Fatalf("excess guardians in ordinary lanes = %d, want 3", guardianLater)
	}
}

func TestDisabledGuardiansStayInOrdinaryLanes(t *testing.T) {
	validator := accAddrN(1)
	addr := accAddrN(10)
	snap := manualSnap(validator, map[string]int64{addr: 1024})
	snap.guardians = map[string]struct{}{addr: {}}
	items := []participantWork{workItem(addr, "model-a", 1)}
	_, lanes := plan(items, snap, 4)
	if lanes[0] == laneGuardian {
		t.Fatal("disabled guardian entered the prefix")
	}
}

func TestZeroPowerGuardianCanTakePrefixThenAdmission(t *testing.T) {
	validator := accAddrN(1)
	guardian := accAddrN(10)
	snap := manualSnap(validator, map[string]int64{accAddrN(11): 4})
	snap.guardianEnabled = true
	snap.guardians = map[string]struct{}{guardian: {}}
	items := []participantWork{
		workItem(guardian, "model-a", 1),
		workItem(accAddrN(11), "model-a", 2),
		workItem(accAddrN(12), "other", 3),
	}
	ordered, lanes := plan(items, snap, 1)
	assertPermutation(t, items, ordered)
	if lanes[0] != laneGuardian || ordered[0].address != guardian {
		t.Fatalf("first = %s lane %s", ordered[0].address, lanes[0])
	}
	if countLane(lanes, laneGuardian) != 1 {
		t.Fatal("zero-power guardian was prefixed more than once")
	}
}

func TestImpactLanePrefersHigherBands(t *testing.T) {
	validator := accAddrN(1)
	high := accAddrN(2)
	mid := accAddrN(3)
	low := accAddrN(4)
	snap := manualSnap(validator, map[string]int64{high: 2048, mid: 16, low: 1})
	items := []participantWork{
		workItem(low, "model-a", 1),
		workItem(high, "model-a", 2),
		workItem(mid, "model-a", 3),
	}
	ordered, lanes := plan(items, snap, 1)
	var bands []uint8
	for i, item := range ordered {
		if lanes[i] != laneImpact {
			continue
		}
		bands = append(bands, powerBand(modelPower(snap, item.modelId, item.address)))
	}
	if len(bands) != 3 {
		t.Fatalf("impact items = %d", len(bands))
	}
	for i := 1; i < len(bands); i++ {
		if bands[i] > bands[i-1] {
			t.Fatalf("impact bands not descending: %v", bands)
		}
	}
	if bands[0] != powerBand(2048) {
		t.Fatalf("first impact band = %d", bands[0])
	}
}

func TestCoverageRoundRobinUsesEveryNonEmptyBand(t *testing.T) {
	groups := map[uint8][]validationOrderItem{
		1: {{index: 0, powerBand: 1}, {index: 1, powerBand: 1}},
		2: {{index: 2, powerBand: 2}},
		3: {{index: 3, powerBand: 3}, {index: 4, powerBand: 3}, {index: 5, powerBand: 3}},
	}
	got := roundRobinByBand([]uint8{2, 3, 1}, groups)
	want := []uint8{2, 3, 1, 3, 1, 3}
	if len(got) != len(want) {
		t.Fatalf("len %d", len(got))
	}
	seen := map[uint8]int{}
	for i, item := range got {
		if item.powerBand != want[i] {
			t.Fatalf("position %d band %d, want %d", i, item.powerBand, want[i])
		}
		seen[item.powerBand]++
	}
	if len(seen) != 3 {
		t.Fatalf("coverage occupied by %d bands", len(seen))
	}
}

func TestCoverageConstructionDoesNotDropBands(t *testing.T) {
	validator := accAddrN(1)
	snap := manualSnap(validator, nil)
	var items []participantWork
	for band, power := range []int64{1, 2, 8, 64} {
		for n := 0; n < 3; n++ {
			addr := accAddrN(100 + band*10 + n)
			snap.modelVotingPower["model-a"][addr] = power
			items = append(items, workItem(addr, "model-a", uint32(len(items))))
		}
	}
	meta := deriveValidationOrder(snap, items)
	coverage := establishedByCoverage(meta, snap)
	if len(coverage) != len(items) {
		t.Fatalf("coverage len %d, want %d", len(coverage), len(items))
	}
	seen := map[uint8]int{}
	for _, item := range coverage {
		seen[item.powerBand]++
	}
	if len(seen) != 4 {
		t.Fatalf("bands in coverage = %d", len(seen))
	}
	rotations := map[uint8]int{}
	for n := 0; n < 24; n++ {
		other := manualSnap(accAddrN(200+n), snap.modelVotingPower["model-a"])
		other.stage = snap.stage
		copy(other.appHash, snap.appHash)
		rotations[uint8(coverageRotationOffset(other, 4))]++
	}
	if len(rotations) < 2 {
		t.Fatalf("rotation did not vary across validators: %v", rotations)
	}
}

func TestLaneRatioKeepsLowerBandAndAdmission(t *testing.T) {
	validator := accAddrN(1)
	snap := manualSnap(validator, nil)
	var items []participantWork
	for i := 0; i < 16; i++ {
		addr := accAddrN(20 + i)
		snap.modelVotingPower["model-a"][addr] = 2048
		items = append(items, workItem(addr, "model-a", uint32(i)))
	}
	for i := 0; i < 16; i++ {
		addr := accAddrN(50 + i)
		snap.modelVotingPower["model-a"][addr] = 2
		items = append(items, workItem(addr, "model-a", uint32(100+i)))
	}
	for i := 0; i < 16; i++ {
		items = append(items, workItem(accAddrN(80+i), "model-a", uint32(200+i)))
	}

	ordered, lanes := plan(items, snap, 10)
	assertPermutation(t, items, ordered)
	for i := 0; i < priorityImpactBurst; i++ {
		if lanes[i] != laneImpact {
			t.Fatalf("position %d lane %s", i, lanes[i])
		}
		if powerBand(modelPower(snap, ordered[i].modelId, ordered[i].address)) != powerBand(2048) {
			t.Fatal("impact burst included a lower band")
		}
	}
	if lanes[priorityImpactBurst] != laneCoverage || lanes[priorityImpactBurst+priorityCoverageBurst] != laneAdmission {
		t.Fatalf("cycle lanes = %v", lanes[:priorityImpactBurst+2])
	}

	high := powerBand(2048)
	highPositions := 0
	lowerCoverage := 0
	for i, item := range ordered {
		band := powerBand(modelPower(snap, item.modelId, item.address))
		if band == high {
			highPositions++
		}
		if lanes[i] == laneCoverage && band > 0 && band < high {
			lowerCoverage++
		}
	}
	if highPositions == len(ordered) {
		t.Fatal("highest band occupied every position")
	}
	if lowerCoverage == 0 {
		t.Fatal("coverage lane never reached the lower band")
	}
	if countLane(lanes, laneAdmission) != 16 {
		t.Fatalf("admission lanes = %d", countLane(lanes, laneAdmission))
	}
}

func TestEmptyLaneDoesNotStall(t *testing.T) {
	validator := accAddrN(1)
	established := manualSnap(validator, map[string]int64{accAddrN(2): 9, accAddrN(3): 1})
	onlyEstablished := []participantWork{workItem(accAddrN(2), "model-a", 1), workItem(accAddrN(3), "model-a", 2)}
	ordered, lanes := plan(onlyEstablished, established, 4)
	assertPermutation(t, onlyEstablished, ordered)
	if countLane(lanes, laneAdmission) != 0 {
		t.Fatal("empty admission lane produced admission positions")
	}

	admissionSnap := manualSnap(validator, map[string]int64{accAddrN(9): 1})
	onlyAdmission := []participantWork{workItem(accAddrN(4), "model-a", 1), workItem("bad", "model-a", 2)}
	ordered, lanes = plan(onlyAdmission, admissionSnap, 4)
	assertPermutation(t, onlyAdmission, ordered)
	if countLane(lanes, laneAdmission) != len(onlyAdmission) {
		t.Fatalf("admission lanes = %v", lanes)
	}

	if ordered, lanes := plan(nil, established, 4); len(ordered) != 0 || len(lanes) != 0 {
		t.Fatal("empty input produced output")
	}
}

func TestOrderKeysAreValidatorStageAndModelSpecific(t *testing.T) {
	first := manualSnap(accAddrN(1), map[string]int64{accAddrN(2): 4})
	second := manualSnap(accAddrN(3), map[string]int64{accAddrN(2): 4})
	second.stage = first.stage
	copy(second.appHash, first.appHash)
	items := []participantWork{workItem(accAddrN(2), "model-a", 1), workItem(accAddrN(2), "model-b", 2)}
	left := deriveValidationOrder(first, items)
	right := deriveValidationOrder(second, items)
	if left[0].impactKey == right[0].impactKey || left[0].coverageKey == right[0].coverageKey || left[0].admissionKey == right[0].admissionKey {
		t.Fatal("validator did not change order keys")
	}
	again := deriveValidationOrder(first, items)
	if left[0].impactKey != again[0].impactKey {
		t.Fatal("order key was not deterministic")
	}
	shifted := *first
	shifted.stage++
	shiftedMeta := deriveValidationOrder(&shifted, items)
	if left[0].impactKey == shiftedMeta[0].impactKey {
		t.Fatal("stage did not change the impact key")
	}
	if left[0].impactKey == left[1].impactKey {
		t.Fatal("model id did not change the impact key")
	}
}

func TestOrderKeyCompareUsesFullWidth(t *testing.T) {
	var low, high [32]byte
	low[31] = 1
	high[31] = 2
	meta := []validationOrderItem{
		{index: 0, powerBand: 4, impactKey: high},
		{index: 1, powerBand: 4, impactKey: low},
	}
	got := establishedByBand(meta)
	if got[0].index != 1 || got[1].index != 0 {
		t.Fatalf("order = %d, %d", got[0].index, got[1].index)
	}
}

func TestNegativeZeroAndMissingPowerStayAdmission(t *testing.T) {
	validator := accAddrN(1)
	negative := accAddrN(2)
	zero := accAddrN(3)
	missing := accAddrN(4)
	snap := manualSnap(validator, map[string]int64{negative: math.MinInt64, zero: 0, accAddrN(5): math.MaxInt64})
	items := []participantWork{
		workItem(negative, "model-a", 1),
		workItem(zero, "model-a", 2),
		workItem(missing, "model-a", 3),
		workItem("", "model-a", 4),
		workItem(accAddrN(5), "model-a", 5),
	}
	meta := deriveValidationOrder(snap, items)
	for i := 0; i < 4; i++ {
		if meta[i].powerBand != 0 || meta[i].guardian {
			t.Fatalf("item %d band %d guardian %v", i, meta[i].powerBand, meta[i].guardian)
		}
	}
	if meta[4].powerBand != 63 {
		t.Fatalf("max int64 band = %d", meta[4].powerBand)
	}
	ordered, _ := plan(items, snap, 2)
	assertPermutation(t, items, ordered)
}

func TestMalformedMetadataStillPermutes(t *testing.T) {
	items := []participantWork{
		workItem("a", "m", 0),
		workItem("b", "m", 1),
		workItem("a", "m", 2),
	}
	ordered, lanes := prioritizeValidationWork(items, nil, nil, 4)
	assertPermutation(t, items, ordered)
	if !sameWorkOrder(items, ordered) {
		t.Fatalf("defensive order = %#v", ordered)
	}
	if len(lanes) != 3 {
		t.Fatalf("lanes %d", len(lanes))
	}

	duplicate := []validationOrderItem{{index: 0, powerBand: 3}, {index: 0, powerBand: 3}}
	ordered, _ = prioritizeValidationWork(items, nil, duplicate, 1)
	assertPermutation(t, items, ordered)
}

func TestZeroWorkerCountSkipsGuardianPrefix(t *testing.T) {
	validator := accAddrN(1)
	guardian := accAddrN(2)
	snap := manualSnap(validator, map[string]int64{guardian: 100})
	snap.guardianEnabled = true
	snap.guardians = map[string]struct{}{guardian: {}}
	_, lanes := plan([]participantWork{workItem(guardian, "model-a", 1)}, snap, 0)
	if countLane(lanes, laneGuardian) != 0 {
		t.Fatal("worker count 0 still prefixed a guardian")
	}
	_, lanes = plan([]participantWork{workItem(guardian, "model-a", 1)}, snap, -3)
	if countLane(lanes, laneGuardian) != 0 {
		t.Fatal("negative worker count still prefixed a guardian")
	}
}

func FuzzPrioritizeValidationWork(f *testing.F) {
	f.Add(int64(100), uint8(5), int64(1024), uint8(4))
	f.Add(int64(0), uint8(0), int64(-1), uint8(0))
	f.Fuzz(func(t *testing.T, stage int64, nRaw uint8, power int64, workersRaw uint8) {
		n := int(nRaw % 24)
		validator := accAddrN(1)
		items := make([]participantWork, n)
		for i := range items {
			items[i] = participantWork{
				address: fmt.Sprintf("raw-%d", i%5),
				modelId: fmt.Sprintf("m-%d", i%3),
				count:   uint32(i),
			}
			if i%7 == 0 {
				items[i].address = ""
			}
		}
		if n > 0 {
			items[0].address = accAddrN(2)
			items[0].modelId = "model-a"
		}
		resp := authorityFixture(stage, validator, []*types.VotingPowerEntry{{
			Address:     accAddrN(2),
			VotingPower: power,
		}})
		resp.Snapshot.PocStageStartHeight = stage
		resp.Snapshot.SnapshotHeight = 1
		snap, reason := authoritativePrioritySnapshot(stage, validator, resp)
		if power <= 0 {
			if reason == "" || snap != nil {
				t.Fatalf("non-positive power reason=%q", reason)
			}
			return
		}
		if reason != "" || snap == nil {
			t.Fatalf("reason %q", reason)
		}
		before := append([]participantWork(nil), items...)
		ordered, lanes := plan(items, snap, int(workersRaw)-8)
		assertPermutation(t, items, ordered)
		if !sameWorkOrder(items, before) {
			t.Fatal("input mutated")
		}
		if len(lanes) != len(items) {
			t.Fatalf("lanes %d", len(lanes))
		}
	})
}

func TestOrderOrdinaryValidationDispatchesPriority(t *testing.T) {
	stage := int64(100)
	validator := accAddrN(1)
	low := accAddrN(2)
	high := accAddrN(3)
	resp := authorityFixture(stage, validator, []*types.VotingPowerEntry{
		{Address: low, VotingPower: 2},
		{Address: high, VotingPower: 2048},
	})
	items := []participantWork{
		workItem(low, "model-a", 1),
		workItem(accAddrN(4), "model-a", 2),
		workItem(high, "model-a", 3),
	}
	before := append([]participantWork(nil), items...)
	ordered, decision := orderOrdinaryValidationWork(stage, validator, resp, items, 4, nil)
	if decision.Mode != "priority" || decision.Reason != "" {
		t.Fatalf("mode %s reason %s", decision.Mode, decision.Reason)
	}
	if ordered[0].address != high {
		t.Fatalf("first dispatch = %s, want high-power target", ordered[0].address)
	}
	assertPermutation(t, before, ordered)
	if !sameWorkOrder(items, before) {
		t.Fatal("priority dispatch reordered the caller's slice")
	}
	if decision.Established != 2 || decision.Admission != 1 || decision.Guardian != 0 {
		t.Fatalf("classes %+v", decision)
	}
	if decision.LaneGuardian != 0 {
		t.Fatal("chain snapshot opened the guardian prefix")
	}
	if decision.LaneImpact+decision.LaneCoverage+decision.LaneAdmission != len(items) {
		t.Fatalf("lanes %+v", decision)
	}
}

func TestOrderOrdinaryValidationShufflesIncompleteSnapshot(t *testing.T) {
	stage := int64(100)
	validator := accAddrN(1)
	items := make([]participantWork, 8)
	for i := range items {
		items[i] = workItem(accAddrN(10+i), "model-a", uint32(i))
	}
	expected := append([]participantWork(nil), items...)
	rng := rand.New(rand.NewSource(7))
	ordered, decision := orderOrdinaryValidationWork(stage, validator, generationStartStub(stage), items, 4, rng)
	rand.New(rand.NewSource(7)).Shuffle(len(expected), func(i, j int) {
		expected[i], expected[j] = expected[j], expected[i]
	})
	if decision.Mode != "shuffle" || decision.Reason != string(fallbackSnapshotPartial) {
		t.Fatalf("mode %s reason %s", decision.Mode, decision.Reason)
	}
	if !sameWorkOrder(ordered, expected) {
		t.Fatal("fallback shuffle did not match the seeded shuffle")
	}
	assertPermutation(t, expected, ordered)

	queryFailed, queryDecision := orderOrdinaryValidationWork(stage, validator, nil, append([]participantWork(nil), items...), 4, rand.New(rand.NewSource(1)))
	if queryDecision.Mode != "shuffle" || queryDecision.Reason != string(fallbackSnapshotMissing) || len(queryFailed) != len(items) {
		t.Fatalf("query failure mode %s reason %s len %d", queryDecision.Mode, queryDecision.Reason, len(queryFailed))
	}
}

func plan(items []participantWork, snap *prioritySnapshot, workers int) ([]participantWork, []validationLane) {
	return prioritizeValidationWork(items, snap, deriveValidationOrder(snap, items), workers)
}

func manualSnap(validator string, powers map[string]int64) *prioritySnapshot {
	addr, err := types.CanonicalAccAddress(validator)
	if err != nil {
		panic(err)
	}
	copied := make(map[string]int64, len(powers))
	for address, power := range powers {
		copied[address] = power
	}
	return &prioritySnapshot{
		stage:            42,
		appHash:          bytes.Repeat([]byte{0xab}, 32),
		modelVotingPower: map[string]map[string]int64{"model-a": copied},
		validator:        append([]byte(nil), addr...),
	}
}

func mixedWork(t *testing.T) ([]participantWork, *prioritySnapshot) {
	t.Helper()
	high := accAddrN(2)
	low := accAddrN(3)
	snap := manualSnap(accAddrN(1), map[string]int64{high: 100, low: 2})
	items := []participantWork{
		workItem(high, "model-a", 1),
		workItem(low, "model-a", 2),
		workItem(accAddrN(4), "model-a", 3),
		workItem("not-bech32", "model-b", 4),
	}
	return items, snap
}

func sampleWork(validator string) []participantWork {
	return []participantWork{workItem(validator, "model-a", 1)}
}

func withAppHash(resp *types.QueryPoCValidationSnapshotResponse, appHash string) *types.QueryPoCValidationSnapshotResponse {
	clone := cloneResp(resp)
	clone.Snapshot.AppHash = appHash
	return clone
}

func withHeight(resp *types.QueryPoCValidationSnapshotResponse, height int64) *types.QueryPoCValidationSnapshotResponse {
	clone := cloneResp(resp)
	clone.Snapshot.SnapshotHeight = height
	return clone
}

func withModels(resp *types.QueryPoCValidationSnapshotResponse, models []*types.ModelVotingPowers) *types.QueryPoCValidationSnapshotResponse {
	clone := cloneResp(resp)
	clone.Snapshot.ModelVotingPowers = models
	return clone
}

func cloneResp(resp *types.QueryPoCValidationSnapshotResponse) *types.QueryPoCValidationSnapshotResponse {
	clone := *resp
	snapshot := *resp.Snapshot
	clone.Snapshot = &snapshot
	return &clone
}

func assertPermutation(t *testing.T, items, ordered []participantWork) {
	t.Helper()
	if len(items) != len(ordered) {
		t.Fatalf("len %d != %d", len(ordered), len(items))
	}
	got := make(map[string]int, len(ordered))
	for _, item := range ordered {
		got[workSig(item)]++
	}
	for _, item := range items {
		sig := workSig(item)
		if got[sig] == 0 {
			t.Fatalf("missing %s", sig)
		}
		got[sig]--
	}
	for sig, n := range got {
		if n != 0 {
			t.Fatalf("extra %s (%d)", sig, n)
		}
	}
}

func sameWorkOrder(left, right []participantWork) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if workSig(left[i]) != workSig(right[i]) {
			return false
		}
	}
	return true
}

func workSig(item participantWork) string {
	return fmt.Sprintf("%s|%s|%d", item.address, item.modelId, item.count)
}

func countLane(lanes []validationLane, want validationLane) int {
	n := 0
	for _, lane := range lanes {
		if lane == want {
			n++
		}
	}
	return n
}
