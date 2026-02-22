package keeper

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	StatsModelUsageBySecond = "stats/model/usage/second"
	StatsModelUsageMeta     = "stats/model/usage/meta"
)

var modelUsageSeparator = []byte("__MODEL__")
var modelUsageCutoverKey = []byte("cutover_ms")

type modelUsageBucket struct {
	InferenceCount uint64
	TokensUsed     uint64
	ActualCost     uint64
}

// AddModelUsageSample stores lightweight per-model usage in per-second buckets.
// This data is intentionally compact and is used by on-chain consumers such as
// dynamic pricing and invalidation limits.
func (k Keeper) AddModelUsageSample(
	ctx context.Context,
	model string,
	timestampMillis int64,
	tokenCount uint64,
	actualCost int64,
) error {
	if model == "" || timestampMillis <= 0 {
		return nil
	}

	bucketSecond := uint64(timestampMillis / 1000)
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	usageStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsModelUsageBySecond))
	metaStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsModelUsageMeta))

	if metaStore.Get(modelUsageCutoverKey) == nil {
		metaStore.Set(modelUsageCutoverKey, sdk.Uint64ToBigEndian(uint64(timestampMillis)))
	}

	key := modelUsageKey(bucketSecond, model)
	bucket := modelUsageBucket{}

	if value := usageStore.Get(key); value != nil {
		var err error
		bucket, err = unmarshalModelUsageBucket(value)
		if err != nil {
			return err
		}
	}

	if err := addUint64(&bucket.InferenceCount, 1); err != nil {
		return err
	}
	if err := addUint64(&bucket.TokensUsed, tokenCount); err != nil {
		return err
	}
	if actualCost > 0 {
		if err := addUint64(&bucket.ActualCost, uint64(actualCost)); err != nil {
			return err
		}
	}

	usageStore.Set(key, marshalModelUsageBucket(bucket))
	return nil
}

func (k Keeper) GetModelUsageCutoverTimestamp(ctx context.Context) (int64, bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	metaStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsModelUsageMeta))
	value := metaStore.Get(modelUsageCutoverKey)
	if len(value) != 8 {
		return 0, false
	}
	cutover := sdk.BigEndianToUint64(value)
	maxInt64AsUint64 := ^uint64(0) >> 1
	if cutover > maxInt64AsUint64 {
		return 0, false
	}
	return int64(cutover), true
}

// GetModelUsageSummaryByTime returns aggregated model usage from lightweight
// per-second buckets for the requested millisecond range (inclusive).
func (k Keeper) GetModelUsageSummaryByTime(ctx context.Context, from, to int64) map[string]StatsSummary {
	result := make(map[string]StatsSummary)
	if to < from {
		return result
	}

	if from < 0 {
		from = 0
	}
	if to < 0 {
		to = 0
	}

	startSecond := uint64(from / 1000)
	endSecond := uint64(to / 1000)

	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	usageStore := prefix.NewStore(storeAdapter, types.KeyPrefix(StatsModelUsageBySecond))

	start := sdk.Uint64ToBigEndian(startSecond)

	var end []byte
	if endSecond == math.MaxUint64 {
		end = nil
	} else {
		end = sdk.Uint64ToBigEndian(endSecond + 1)
	}

	iterator := usageStore.Iterator(start, end)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		model, ok := parseModelUsageModel(iterator.Key())
		if !ok {
			continue
		}

		bucket, err := unmarshalModelUsageBucket(iterator.Value())
		if err != nil {
			k.LogError("Unable to unmarshal model usage bucket", types.Stat, "key", iterator.Key(), "error", err)
			continue
		}

		summary := result[model]
		summary.InferenceCount += int(bucket.InferenceCount)
		summary.TokensUsed += int64(bucket.TokensUsed)
		summary.ActualCost += int64(bucket.ActualCost)
		result[model] = summary
	}

	return result
}

func modelUsageKey(bucketSecond uint64, model string) []byte {
	key := append([]byte{}, sdk.Uint64ToBigEndian(bucketSecond)...)
	key = append(key, modelUsageSeparator...)
	key = append(key, []byte(model)...)
	return key
}

func parseModelUsageModel(key []byte) (string, bool) {
	separatorStart := 8
	separatorEnd := separatorStart + len(modelUsageSeparator)
	if len(key) <= separatorEnd {
		return "", false
	}
	if !bytes.Equal(key[separatorStart:separatorEnd], modelUsageSeparator) {
		return "", false
	}
	return string(key[separatorEnd:]), true
}

func marshalModelUsageBucket(bucket modelUsageBucket) []byte {
	out := make([]byte, 24)
	binary.BigEndian.PutUint64(out[0:8], bucket.InferenceCount)
	binary.BigEndian.PutUint64(out[8:16], bucket.TokensUsed)
	binary.BigEndian.PutUint64(out[16:24], bucket.ActualCost)
	return out
}

func unmarshalModelUsageBucket(data []byte) (modelUsageBucket, error) {
	if len(data) != 24 {
		return modelUsageBucket{}, fmt.Errorf("unexpected model usage bucket size: %d", len(data))
	}
	return modelUsageBucket{
		InferenceCount: binary.BigEndian.Uint64(data[0:8]),
		TokensUsed:     binary.BigEndian.Uint64(data[8:16]),
		ActualCost:     binary.BigEndian.Uint64(data[16:24]),
	}, nil
}

func addUint64(dst *uint64, delta uint64) error {
	if math.MaxUint64-*dst < delta {
		return fmt.Errorf("uint64 overflow: current=%d delta=%d", *dst, delta)
	}
	*dst += delta
	return nil
}
