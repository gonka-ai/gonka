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
) Params {
	return Params{
		ITotalSlots:                     iTotalSlots,
		TSlotsDegreeOffset:              tSlotsDegreeOffset,
		DealingPhaseDurationBlocks:      dealingPhaseDurationBlocks,
		VerificationPhaseDurationBlocks: verificationPhaseDurationBlocks,
		SigningDeadlineBlocks:           signingDeadlineBlocks,
	}
}

// DefaultParams returns a default set of parameters for PoC
func DefaultParams() Params {
	return NewParams(
		100, // i_total_slots: 100 for PoC (smaller than production 1000)
		50,  // t_slots_degree_offset: floor(100/2) = 50
		5,   // dealing_phase_duration_blocks: 5 blocks for PoC
		3,   // verification_phase_duration_blocks: 3 blocks for PoC
		10,  // signing_deadline_blocks: 10 blocks for PoC (enough time for controllers to respond)
	)
}

// ParamSetPairs get the params.ParamSet
func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyITotalSlots, &p.ITotalSlots, validateITotalSlots),
		paramtypes.NewParamSetPair(KeyTSlotsDegreeOffset, &p.TSlotsDegreeOffset, validateTSlotsDegreeOffset),
		paramtypes.NewParamSetPair(KeyDealingPhaseDurationBlocks, &p.DealingPhaseDurationBlocks, validateDealingPhaseDurationBlocks),
		paramtypes.NewParamSetPair(KeyVerificationPhaseDurationBlocks, &p.VerificationPhaseDurationBlocks, validateVerificationPhaseDurationBlocks),
		paramtypes.NewParamSetPair(KeySigningDeadlineBlocks, &p.SigningDeadlineBlocks, validateSigningDeadlineBlocks),
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
