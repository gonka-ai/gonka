package types

import "cosmossdk.io/collections"

// Collections prefixes for dynamic pricing storage
var (
	DynamicPricingCurrentPrefix  = collections.NewPrefix(4)
	DynamicPricingCapacityPrefix = collections.NewPrefix(5)
)
