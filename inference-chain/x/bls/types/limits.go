package types

// Module-wide stateless validation limits for BLS
// Note: These limits only enforce shapes/sizes and do not read any state.

const (
	// Generic upper bounds for repeated fields (DOS guards)
	// Adjust as needed based on protocol expectations
	// A note about these
	MaxParticipantCount = 10000
	MaxSlotIndices      = 10000
)
