package types

const (
	// ModuleName defines the module name
	ModuleName = "restrictions"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_restrictions"
)

var (
	ParamsKey                  = []byte("p_restrictions")
	KeyRestrictionUnregistered = []byte("restriction_unregistered")
)

// Event types and attribute keys
const (
	EventTypeEmergencyTransfer = "emergency_transfer"
	EventTypeRestrictionLifted = "restriction_lifted"

	AttributeKeyExemptionId         = "exemption_id"
	AttributeKeyFromAddress         = "from_address"
	AttributeKeyToAddress           = "to_address"
	AttributeKeyAmount              = "amount"
	AttributeKeyDenom               = "denom"
	AttributeKeyRemainingUses       = "remaining_uses"
	AttributeKeyCurrentBlock        = "current_block"
	AttributeKeyRestrictionEndBlock = "restriction_end_block"
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}
