package inference

import (
	"github.com/productscience/inference/x/inference/types"
)

// MigrationState represents the current V1/V2 mode based on governance parameters.
type MigrationState int

const (
	ModeFullV1    MigrationState = iota // poc_v2_enabled=false, confirmation_poc_v2_enabled=false
	ModeMigration                       // poc_v2_enabled=false, confirmation_poc_v2_enabled=true
	ModeFullV2                          // poc_v2_enabled=true (confirmation_poc_v2_enabled must also be true)
)

// GetMigrationState determines current mode from params.
// Returns ModeFullV2 for invalid combinations (poc_v2=true, confirmation_v2=false).
func GetMigrationState(pocV2Enabled, confirmationPocV2Enabled bool) MigrationState {
	if pocV2Enabled {
		return ModeFullV2
	}
	if confirmationPocV2Enabled {
		return ModeMigration
	}
	return ModeFullV1
}

// GetMigrationStateFromParams extracts migration state from PocParams.
func GetMigrationStateFromParams(params *types.PocParams) MigrationState {
	if params == nil {
		return ModeFullV2 // default
	}
	return GetMigrationState(params.PocV2Enabled, params.ConfirmationPocV2Enabled)
}
