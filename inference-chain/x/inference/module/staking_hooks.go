package inference

import (
	"context"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"log"
)

type StakingHooksLogger struct{}

func (s StakingHooksLogger) AfterUnbondingInitiated(ctx context.Context, id uint64) error {
	log.Println("SHL:", "AfterUnbondingInitiated:", id)
	return nil
}

func (s StakingHooksLogger) AfterValidatorCreated(ctx context.Context, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "AfterValidatorCreated:", valAddr.String())
	return nil
}

func (s StakingHooksLogger) BeforeValidatorModified(ctx context.Context, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "BeforeValidatorModified:", valAddr.String())
	return nil
}

func (s StakingHooksLogger) AfterValidatorRemoved(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "AfterValidatorRemoved:", consAddr.String(), ":", valAddr.String())
	return nil
}

func (s StakingHooksLogger) AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "AfterValidatorBonded:", consAddr.String(), ":", valAddr.String())
	return nil
}

func (s StakingHooksLogger) AfterValidatorBeginUnbonding(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "AfterValidatorBeginUnbonding:", consAddr.String(), ":", valAddr.String())
	return nil
}

func (s StakingHooksLogger) BeforeDelegationCreated(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "BeforeDelegationCreated:", delAddr.String(), ":", valAddr.String())
	return nil
}

func (s StakingHooksLogger) BeforeDelegationSharesModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "BeforeDelegationSharesModified:", delAddr.String(), ":", valAddr.String())
	return nil
}

func (s StakingHooksLogger) BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "BeforeDelegationRemoved:", delAddr.String(), ":", valAddr.String())
	return nil
}

func (s StakingHooksLogger) AfterDelegationModified(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress) error {
	log.Println("SHL:", "AfterDelegationModified:", delAddr.String(), ":", valAddr.String())
	return nil
}

func (s StakingHooksLogger) BeforeValidatorSlashed(ctx context.Context, valAddr sdk.ValAddress, fraction math.LegacyDec) error {
	log.Println("SHL:", "BeforeValidatorSlashed:", valAddr.String(), ":", fraction.String())
	return nil
}
