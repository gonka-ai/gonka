package types

const (
	// DynamicPricingCurrentKeyPrefix is the prefix for storing current per-token prices per model
	DynamicPricingCurrentKeyPrefix = "pricing/current/"

	// DynamicPricingCapacityKeyPrefix is the prefix for storing cached model capacity data
	DynamicPricingCapacityKeyPrefix = "pricing/capacity/"
)

// DynamicPricingCurrentKey returns the key for storing current per-token price for a model
func DynamicPricingCurrentKey(modelId string) []byte {
	return StringKey(modelId)
}

// DynamicPricingCurrentFullKey returns the full key path for current pricing
func DynamicPricingCurrentFullKey(modelId string) []byte {
	key := DynamicPricingCurrentKeyPrefix + modelId
	return []byte(key)
}

// DynamicPricingCapacityKey returns the key for storing cached capacity for a model
func DynamicPricingCapacityKey(modelId string) []byte {
	return StringKey(modelId)
}

// DynamicPricingCapacityFullKey returns the full key path for capacity caching
func DynamicPricingCapacityFullKey(modelId string) []byte {
	key := DynamicPricingCapacityKeyPrefix + modelId
	return []byte(key)
}
