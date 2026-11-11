package types

import (
	"fmt"

	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
)

var _ paramtypes.ParamSet = (*Params)(nil)

// Parameter store keys
var (
	KeyITotalSlots                     = []byte("ITotalSlots")
	KeyTSlotsDegreeOffset              = []byte("TSlotsDegreeOffset")
	KeyDealingPhaseDurationBlocks      = []byte("DealingPhaseDurationBlocks")
	KeyVerificationPhaseDurationBlocks = []byte("VerificationPhaseDurationBlocks")
	KeySigningDeadlineBlocks           = []byte("SigningDeadlineBlocks")
	KeyDisputePhaseDurationBlocks      = []byte("DisputePhaseDurationBlocks")
	KeyCompletedFallbackBlocks         = []byte("CompletedFallbackBlocks")
	KeyMaxSigningAttempts              = []byte("MaxSigningAttempts")
	KeyBeaconKeySwitchDelay            = []byte("BeaconKeySwitchDelay")
	KeyBeaconRetentionBlocks           = []byte("BeaconRetentionBlocks")
)

const (
	// DefaultBeaconKeySwitchDelay: blocks between a DKG's completion and the
	// first beacon round it signs (rule R9). Covers dAPI export + node reload.
	DefaultBeaconKeySwitchDelay uint64 = 5
	// MaxBeaconKeySwitchDelay bounds the delay so a new key cannot be postponed
	// indefinitely by a parameter change.
	MaxBeaconKeySwitchDelay uint64 = 1000
	// DefaultBeaconRetentionBlocks keeps beacon records for about two epochs
	// (mainnet epochs are ~15,400 blocks). 0 keeps them forever.
	DefaultBeaconRetentionBlocks uint64 = 31000
)

// ParamKeyTable the param key table for launch module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams(
	iTotalSlots uint32,
	tSlotsDegreeOffset uint32,
	dealingPhaseDurationBlocks int64,
	verificationPhaseDurationBlocks int64,
	signingDeadlineBlocks int64,
	disputePhaseDurationBlocks int64,
	completedFallbackBlocks int64,
	maxSigningAttempts uint32,
) Params {
	return Params{
		ITotalSlots:                     iTotalSlots,
		TSlotsDegreeOffset:              tSlotsDegreeOffset,
		DealingPhaseDurationBlocks:      dealingPhaseDurationBlocks,
		VerificationPhaseDurationBlocks: verificationPhaseDurationBlocks,
		SigningDeadlineBlocks:           signingDeadlineBlocks,
		DisputePhaseDurationBlocks:      disputePhaseDurationBlocks,
		CompletedFallbackBlocks:         completedFallbackBlocks,
		MaxSigningAttempts:              maxSigningAttempts,
	}
}

// DefaultParams returns a default set of parameters for PoC
func DefaultParams() Params {
	p := NewParams(
		100, // i_total_slots: 100 for PoC (smaller than production 1000)
		50,  // t_slots_degree_offset: floor(100/2) = 50
		5,   // dealing_phase_duration_blocks: 5 blocks for PoC
		3,   // verification_phase_duration_blocks: 3 blocks for PoC
		10,  // signing_deadline_blocks: 10 blocks for PoC (enough time for controllers to respond)
		3,   // dispute_phase_duration_blocks: 3 blocks for PoC
		0,   // completed_fallback_blocks: disabled by default
		3,   // max_signing_attempts: initial attempt + 2 retries
	)
	p.BeaconKeySwitchDelay = DefaultBeaconKeySwitchDelay
	p.BeaconRetentionBlocks = DefaultBeaconRetentionBlocks
	return p
}

// ParamSetPairs get the params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyITotalSlots, &p.ITotalSlots, validateITotalSlots),
		paramtypes.NewParamSetPair(KeyTSlotsDegreeOffset, &p.TSlotsDegreeOffset, validateTSlotsDegreeOffset),
		paramtypes.NewParamSetPair(KeyDealingPhaseDurationBlocks, &p.DealingPhaseDurationBlocks, validateDealingPhaseDurationBlocks),
		paramtypes.NewParamSetPair(KeyVerificationPhaseDurationBlocks, &p.VerificationPhaseDurationBlocks, validateVerificationPhaseDurationBlocks),
		paramtypes.NewParamSetPair(KeySigningDeadlineBlocks, &p.SigningDeadlineBlocks, validateSigningDeadlineBlocks),
		paramtypes.NewParamSetPair(KeyDisputePhaseDurationBlocks, &p.DisputePhaseDurationBlocks, validateDisputePhaseDurationBlocks),
		paramtypes.NewParamSetPair(KeyCompletedFallbackBlocks, &p.CompletedFallbackBlocks, validateCompletedFallbackBlocks),
		paramtypes.NewParamSetPair(KeyMaxSigningAttempts, &p.MaxSigningAttempts, validateMaxSigningAttempts),
		paramtypes.NewParamSetPair(KeyBeaconKeySwitchDelay, &p.BeaconKeySwitchDelay, validateBeaconKeySwitchDelay),
		paramtypes.NewParamSetPair(KeyBeaconRetentionBlocks, &p.BeaconRetentionBlocks, func(interface{}) error { return nil }),
	}
}

// Validate validates the set of params
func (p Params) Validate() error {
	if err := validateITotalSlots(p.ITotalSlots); err != nil {
		return err
	}
	if err := validateTSlotsDegreeOffset(p.TSlotsDegreeOffset); err != nil {
		return err
	}
	if err := validateDealingPhaseDurationBlocks(p.DealingPhaseDurationBlocks); err != nil {
		return err
	}
	if err := validateVerificationPhaseDurationBlocks(p.VerificationPhaseDurationBlocks); err != nil {
		return err
	}
	if err := validateSigningDeadlineBlocks(p.SigningDeadlineBlocks); err != nil {
		return err
	}
	if err := validateDisputePhaseDurationBlocks(p.DisputePhaseDurationBlocks); err != nil {
		return err
	}
	if err := validateCompletedFallbackBlocks(p.CompletedFallbackBlocks); err != nil {
		return err
	}
	if err := validateMaxSigningAttempts(p.MaxSigningAttempts); err != nil {
		return err
	}
	if err := validateBeaconKeySwitchDelay(p.BeaconKeySwitchDelay); err != nil {
		return err
	}

	// Additional cross-parameter validation
	if p.TSlotsDegreeOffset >= p.ITotalSlots {
		return fmt.Errorf("t_slots_degree_offset (%d) must be less than i_total_slots (%d)", p.TSlotsDegreeOffset, p.ITotalSlots)
	}

	return nil
}

// Validation functions
func validateITotalSlots(i interface{}) error {
	v, ok := i.(uint32)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v == 0 {
		return fmt.Errorf("i_total_slots must be positive")
	}

	if v < 2 {
		return fmt.Errorf("i_total_slots must be at least 2")
	}

	return nil
}

func validateTSlotsDegreeOffset(i interface{}) error {
	v, ok := i.(uint32)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v == 0 {
		return fmt.Errorf("t_slots_degree_offset must be positive")
	}

	return nil
}

func validateDealingPhaseDurationBlocks(i interface{}) error {
	v, ok := i.(int64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v <= 0 {
		return fmt.Errorf("dealing_phase_duration_blocks must be positive")
	}

	return nil
}

func validateVerificationPhaseDurationBlocks(i interface{}) error {
	v, ok := i.(int64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v <= 0 {
		return fmt.Errorf("verification_phase_duration_blocks must be positive")
	}

	return nil
}

func validateSigningDeadlineBlocks(i interface{}) error {
	v, ok := i.(int64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v <= 0 {
		return fmt.Errorf("signing_deadline_blocks must be positive")
	}

	return nil
}

func validateDisputePhaseDurationBlocks(i interface{}) error {
	v, ok := i.(int64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v <= 0 {
		return fmt.Errorf("dispute_phase_duration_blocks must be positive")
	}

	return nil
}

func validateCompletedFallbackBlocks(i interface{}) error {
	v, ok := i.(int64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v < 0 {
		return fmt.Errorf("completed_fallback_blocks must be non-negative")
	}

	return nil
}

func validateMaxSigningAttempts(i interface{}) error {
	v, ok := i.(uint32)

	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}

	if v == 0 {
		return fmt.Errorf("max_signing_attempts must be positive")
	}

	return nil
}

func validateBeaconKeySwitchDelay(i interface{}) error {
	v, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if v > MaxBeaconKeySwitchDelay {
		return fmt.Errorf("beacon_key_switch_delay must be at most %d, got %d", MaxBeaconKeySwitchDelay, v)
	}
	return nil
}
