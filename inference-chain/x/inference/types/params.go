package types

import (
	"fmt"

	"cosmossdk.io/math"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
	"github.com/pkg/errors"
	"github.com/shopspring/decimal"
)

var (
	KeySlashFractionInvalid              = []byte("SlashFractionInvalid")
	KeySlashFractionDowntime             = []byte("SlashFractionDowntime")
	KeyDowntimeMissedPercentageThreshold = []byte("DowntimeMissedPercentageThreshold")
	KeyGracePeriodEndEpoch               = []byte("GracePeriodEndEpoch")
	KeyBaseWeightRatio                   = []byte("BaseWeightRatio")
	KeyCollateralPerWeightUnit           = []byte("CollateralPerWeightUnit")
	// Vesting parameter keys for TokenomicsParams
	KeyWorkVestingPeriod     = []byte("WorkVestingPeriod")
	KeyRewardVestingPeriod   = []byte("RewardVestingPeriod")
	KeyTopMinerVestingPeriod = []byte("TopMinerVestingPeriod")
	// Bitcoin reward parameter keys
	KeyUseBitcoinRewards          = []byte("UseBitcoinRewards")
	KeyInitialEpochReward         = []byte("InitialEpochReward")
	KeyDecayRate                  = []byte("DecayRate")
	KeyGenesisEpoch               = []byte("GenesisEpoch")
	KeyUtilizationBonusFactor     = []byte("UtilizationBonusFactor")
	KeyFullCoverageBonusFactor    = []byte("FullCoverageBonusFactor")
	KeyPartialCoverageBonusFactor = []byte("PartialCoverageBonusFactor")
	// Dynamic pricing parameter keys
	KeyStabilityZoneLowerBound   = []byte("StabilityZoneLowerBound")
	KeyStabilityZoneUpperBound   = []byte("StabilityZoneUpperBound")
	KeyPriceElasticity           = []byte("PriceElasticity")
	KeyUtilizationWindowDuration = []byte("UtilizationWindowDuration")
	KeyMinPerTokenPrice          = []byte("MinPerTokenPrice")
	KeyBasePerTokenPrice         = []byte("BasePerTokenPrice")
	KeyGracePeriodEndEpochDP     = []byte("GracePeriodEndEpochDP")
	KeyGracePeriodPerTokenPrice  = []byte("GracePeriodPerTokenPrice")
)

var _ paramtypes.ParamSet = (*Params)(nil)

// ParamKeyTable the param key table for inference module
func ParamKeyTable() paramtypes.KeyTable {
	return paramtypes.NewKeyTable().RegisterParamSet(&Params{})
}

// NewParams creates a new Params instance
func NewParams() Params {
	return Params{}
}

const million = 1_000_000
const billion = 1_000_000_000
const year = 365 * 24 * 60 * 60

func DefaultGenesisOnlyParams() GenesisOnlyParams {
	return GenesisOnlyParams{
		TotalSupply:                             1_000 * million * billion,
		OriginatorSupply:                        160 * million * billion,
		TopRewardAmount:                         120 * million * billion,
		PreProgrammedSaleAmount:                 120 * million * billion,
		TopRewards:                              3,
		SupplyDenom:                             BaseCoin,
		StandardRewardAmount:                    600 * million * billion,
		TopRewardPeriod:                         year,
		TopRewardPayouts:                        12,
		TopRewardPayoutsPerMiner:                4,
		TopRewardMaxDuration:                    year * 4,
		MaxIndividualPowerPercentage:            DecimalFromFloat(0.25),
		GenesisGuardianEnabled:                  true, // Enable genesis guardian system by default
		GenesisGuardianNetworkMaturityThreshold: 2_000_000,
		GenesisGuardianMultiplier:               DecimalFromFloat(0.52),
		GenesisGuardianAddresses:                []string{}, // Empty by default - must be set in genesis file
	}
}

// DefaultParams returns a default set of parameters
func DefaultParams() Params {
	return Params{
		EpochParams:           DefaultEpochParams(),
		ValidationParams:      DefaultValidationParams(),
		PocParams:             DefaultPocParams(),
		ConfirmationPocParams: DefaultConfirmationPoCParams(),
		TokenomicsParams:      DefaultTokenomicsParams(),
		CollateralParams:      DefaultCollateralParams(),
		BitcoinRewardParams:   DefaultBitcoinRewardParams(),
		DynamicPricingParams:  DefaultDynamicPricingParams(),
		GenesisGuardianParams: &GenesisGuardianParams{
			NetworkMaturityThreshold: 2_000_000,
			NetworkMaturityMinHeight: 0,
			// Note: proto encoding does not preserve empty-vs-nil for repeated fields; keep nil to match round-trips.
			GuardianAddresses: nil,
		},
		DeveloperAccessParams: &DeveloperAccessParams{
			UntilBlockHeight: 0, // disabled by default
			// Note: proto encoding does not preserve empty-vs-nil for repeated fields; keep nil to match round-trips.
			AllowedDeveloperAddresses: nil,
		},
		ParticipantAccessParams: &ParticipantAccessParams{
			NewParticipantRegistrationStartHeight: 0,     // disabled by default
			BlockedParticipantAddresses:           nil,   // keep nil to match proto round-trips
			UseParticipantAllowlist:               false, // disabled by default
			ParticipantAllowlistUntilBlockHeight:  0,     // no cutoff
		},
		TransferAgentAccessParams: &TransferAgentAccessParams{
			// Note: proto encoding does not preserve empty-vs-nil for repeated fields; keep nil to match round-trips.
			AllowedTransferAddresses: nil, // nil = no restriction, all TAs allowed
		},
	}
}

func DefaultEpochParams() *EpochParams {
	return &EpochParams{
		EpochLength:                    40,
		EpochMultiplier:                1,
		EpochShift:                     0,
		DefaultUnitOfComputePrice:      100,
		PocStageDuration:               10,
		PocExchangeDuration:            2,
		PocValidationDelay:             2,
		PocValidationDuration:          6,
		SetNewValidatorsDelay:          1,
		InferenceValidationCutoff:      0,
		InferencePruningEpochThreshold: 2, // Number of epochs after which inferences can be pruned
		PocSlotAllocation: &Decimal{ // Default 0.5 (50%) fraction of nodes allocated to PoC slots
			Value:    5,
			Exponent: -1,
		},
	}
}

func DefaultValidationParams() *ValidationParams {
	return &ValidationParams{
		FalsePositiveRate:              DecimalFromFloat(0.05),
		MinRampUpMeasurements:          10,
		PassValue:                      DecimalFromFloat(0.99),
		MinValidationAverage:           DecimalFromFloat(0.01),
		MaxValidationAverage:           DecimalFromFloat(1.0),
		ExpirationBlocks:               20,
		EpochsToMax:                    30,
		FullValidationTrafficCutoff:    10000,
		MinValidationHalfway:           DecimalFromFloat(0.05),
		MinValidationTrafficCutoff:     100,
		MissPercentageCutoff:           DecimalFromFloat(0.01),
		MissRequestsPenalty:            DecimalFromFloat(1.0),
		TimestampExpiration:            60,
		TimestampAdvance:               30,
		BadParticipantInvalidationRate: DecimalFromFloat(0.20),
		InvalidationHThreshold:         DecimalFromFloat(4),
		InvalidReputationPreserve:      DecimalFromFloat(0.0),
		DowntimeBadPercentage:          DecimalFromFloat(0.20),
		DowntimeGoodPercentage:         DecimalFromFloat(0.1),
		DowntimeHThreshold:             DecimalFromFloat(4),
		DowntimeReputationPreserve:     DecimalFromFloat(0.0),
		QuickFailureThreshold:          DecimalFromFloat(0.000001),
		BinomTestP0:                    DecimalFromFloat(0.10),
	}
}

func DefaultPocParams() *PocParams {
	return &PocParams{
		DefaultDifficulty:            5,
		ValidationSampleSize:         200,
		PocDataPruningEpochThreshold: 1,
		WeightScaleFactor:            DecimalFromFloat(1.0),
		ModelParams:                  DefaultPoCModelParams(), // Deprecated, kept for backward compatibility
		ModelId:                      "",                      // Model identifier for PoC
		SeqLen:                       256,                     // Sequence length for PoC
		StatTest:                     DefaultPoCStatTestParams(),
	}
}

func DefaultPoCStatTestParams() *PoCStatTestParams {
	return &PoCStatTestParams{
		DistThreshold:   DecimalFromFloat(0.4),
		PMismatch:       DecimalFromFloat(0.1),
		PValueThreshold: DecimalFromFloat(0.05),
	}
}

func DefaultPoCModelParams() *PoCModelParams {
	return &PoCModelParams{
		Dim:              1792,
		NLayers:          64,
		NHeads:           64,
		NKvHeads:         64,
		VocabSize:        8196,
		FfnDimMultiplier: DecimalFromFloat(10.0),
		MultipleOf:       4 * 2048,
		NormEps:          DecimalFromFloat(1e-5),
		RopeTheta:        10000,
		UseScaledRope:    false,
		SeqLen:           256,
		RTarget:          DecimalFromFloat(1.398077),
	}
}

func DefaultConfirmationPoCParams() *ConfirmationPoCParams {
	return &ConfirmationPoCParams{
		ExpectedConfirmationsPerEpoch: 0,                     // Feature disabled by default
		AlphaThreshold:                DecimalFromFloat(0.0), // 70% minimum ratio
		SlashFraction:                 DecimalFromFloat(0.0), // 10% slash
		UpgradeProtectionWindow:       500,                   // 500 blocks before/after upgrade
	}
}

func DefaultTokenomicsParams() *TokenomicsParams {
	return &TokenomicsParams{
		SubsidyReductionInterval: DecimalFromFloat(0.05),
		SubsidyReductionAmount:   DecimalFromFloat(0.20),
		CurrentSubsidyPercentage: DecimalFromFloat(0.90),
		TopRewardAllowedFailure:  DecimalFromFloat(0.10),
		TopMinerPocQualification: 10,
		WorkVestingPeriod:        0, // Default: no vesting (production: 180, E2E tests: 2)
		RewardVestingPeriod:      0, // Default: no vesting (production: 180, E2E tests: 2)
		TopMinerVestingPeriod:    0, // Default: no vesting (production: 180, E2E tests: 2)
	}
}

func DefaultCollateralParams() *CollateralParams {
	return &CollateralParams{
		SlashFractionInvalid:              DecimalFromFloat(0.20),
		SlashFractionDowntime:             DecimalFromFloat(0.10),
		DowntimeMissedPercentageThreshold: DecimalFromFloat(0.05),
		GracePeriodEndEpoch:               180,
		BaseWeightRatio:                   DecimalFromFloat(0.2),
		CollateralPerWeightUnit:           DecimalFromFloat(1),
	}
}

func DefaultBitcoinRewardParams() *BitcoinRewardParams {
	return &BitcoinRewardParams{
		UseBitcoinRewards:          true,
		InitialEpochReward:         285000000000000,             // 285,000 gonka coins per epoch (285,000 * 1,000,000,000 ngonka)
		DecayRate:                  DecimalFromFloat(-0.000475), // Exponential decay rate per epoch
		GenesisEpoch:               1,                           // Starting epoch for Bitcoin-style calculations (since epoch 0 is skipped)
		UtilizationBonusFactor:     DecimalFromFloat(0.5),       // Multiplier for utilization bonuses (Phase 2)
		FullCoverageBonusFactor:    DecimalFromFloat(1.2),       // 20% bonus for complete model coverage (Phase 2)
		PartialCoverageBonusFactor: DecimalFromFloat(0.1),       // Scaling factor for partial coverage (Phase 2)
	}
}

func DefaultDynamicPricingParams() *DynamicPricingParams {
	return &DynamicPricingParams{
		StabilityZoneLowerBound:   DecimalFromFloat(0.40), // Lower bound of stability zone (40%)
		StabilityZoneUpperBound:   DecimalFromFloat(0.60), // Upper bound of stability zone (60%)
		PriceElasticity:           DecimalFromFloat(0.05), // Price elasticity factor (5% max change)
		UtilizationWindowDuration: 60,                     // Utilization calculation window (60 seconds)
		MinPerTokenPrice:          1,                      // Minimum per-token price floor (1 ngonka)
		BasePerTokenPrice:         100,                    // Initial per-token price after grace period (100 ngonka)
		GracePeriodEndEpoch:       90,                     // Grace period ends at epoch 90
		GracePeriodPerTokenPrice:  0,                      // Free inference during grace period (0 ngonka)
	}
}

func (p *Params) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{}
}

// ParamSetPairs gets the params for the slashing section
func (p *CollateralParams) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeySlashFractionInvalid, &p.SlashFractionInvalid, validateSlashFraction),
		paramtypes.NewParamSetPair(KeySlashFractionDowntime, &p.SlashFractionDowntime, validateSlashFraction),
		paramtypes.NewParamSetPair(KeyDowntimeMissedPercentageThreshold, &p.DowntimeMissedPercentageThreshold, validatePercentage),
		paramtypes.NewParamSetPair(KeyGracePeriodEndEpoch, &p.GracePeriodEndEpoch, validateEpoch),
		paramtypes.NewParamSetPair(KeyBaseWeightRatio, &p.BaseWeightRatio, validateBaseWeightRatio),
		paramtypes.NewParamSetPair(KeyCollateralPerWeightUnit, &p.CollateralPerWeightUnit, validateCollateralPerWeightUnit),
	}
}

// ParamSetPairs gets the params for the tokenomics vesting parameters
func (p *TokenomicsParams) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyWorkVestingPeriod, &p.WorkVestingPeriod, validateVestingPeriod),
		paramtypes.NewParamSetPair(KeyRewardVestingPeriod, &p.RewardVestingPeriod, validateVestingPeriod),
		paramtypes.NewParamSetPair(KeyTopMinerVestingPeriod, &p.TopMinerVestingPeriod, validateVestingPeriod),
	}
}

// ParamSetPairs gets the params for the Bitcoin reward system
func (p *BitcoinRewardParams) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyUseBitcoinRewards, &p.UseBitcoinRewards, validateUseBitcoinRewards),
		paramtypes.NewParamSetPair(KeyInitialEpochReward, &p.InitialEpochReward, validateInitialEpochReward),
		paramtypes.NewParamSetPair(KeyDecayRate, &p.DecayRate, validateDecayRate),
		paramtypes.NewParamSetPair(KeyGenesisEpoch, &p.GenesisEpoch, validateEpoch),
		paramtypes.NewParamSetPair(KeyUtilizationBonusFactor, &p.UtilizationBonusFactor, validateBonusFactor),
		paramtypes.NewParamSetPair(KeyFullCoverageBonusFactor, &p.FullCoverageBonusFactor, validateBonusFactor),
		paramtypes.NewParamSetPair(KeyPartialCoverageBonusFactor, &p.PartialCoverageBonusFactor, validateBonusFactor),
	}
}

// ParamSetPairs gets the params for the dynamic pricing system
func (p *DynamicPricingParams) ParamSetPairs() paramtypes.ParamSetPairs {
	return paramtypes.ParamSetPairs{
		paramtypes.NewParamSetPair(KeyStabilityZoneLowerBound, &p.StabilityZoneLowerBound, validateStabilityZoneBound),
		paramtypes.NewParamSetPair(KeyStabilityZoneUpperBound, &p.StabilityZoneUpperBound, validateStabilityZoneBound),
		paramtypes.NewParamSetPair(KeyPriceElasticity, &p.PriceElasticity, validatePriceElasticity),
		paramtypes.NewParamSetPair(KeyUtilizationWindowDuration, &p.UtilizationWindowDuration, validateUtilizationWindowDuration),
		paramtypes.NewParamSetPair(KeyMinPerTokenPrice, &p.MinPerTokenPrice, validatePerTokenPrice),
		paramtypes.NewParamSetPair(KeyBasePerTokenPrice, &p.BasePerTokenPrice, validatePerTokenPrice),
		paramtypes.NewParamSetPair(KeyGracePeriodEndEpochDP, &p.GracePeriodEndEpoch, validateEpoch),
		paramtypes.NewParamSetPair(KeyGracePeriodPerTokenPrice, &p.GracePeriodPerTokenPrice, validateGracePeriodPerTokenPrice),
	}
}

func validateEpochParams(i interface{}) error {
	return nil
}

// Validate validates the EpochParams
func (p *EpochParams) Validate() error {
	if p.EpochLength <= 0 {
		return fmt.Errorf("epoch length must be positive")
	}
	if p.EpochMultiplier <= 0 {
		return fmt.Errorf("epoch multiplier must be positive")
	}
	if p.DefaultUnitOfComputePrice < 0 {
		return fmt.Errorf("default unit of compute price cannot be negative")
	}
	if p.PocStageDuration <= 0 {
		return fmt.Errorf("poc stage duration must be positive")
	}
	if p.PocExchangeDuration <= 0 {
		return fmt.Errorf("poc exchange duration must be positive")
	}
	if p.PocValidationDelay < 0 {
		return fmt.Errorf("poc validation delay cannot be negative")
	}
	if p.PocValidationDuration <= 0 {
		return fmt.Errorf("poc validation duration must be positive")
	}
	if p.SetNewValidatorsDelay < 0 {
		return fmt.Errorf("set new validators delay cannot be negative")
	}
	if p.InferenceValidationCutoff < 0 {
		return fmt.Errorf("inference validation cutoff cannot be negative")
	}
	if p.InferencePruningEpochThreshold < 1 {
		return fmt.Errorf("inference pruning epoch threshold must be at least 1")
	}
	return nil
}

// Validate validates the set of params
func (p Params) Validate() error {
	// Check for nil nested structs before calling their Validate() methods
	if p.ValidationParams == nil {
		return fmt.Errorf("validation params cannot be nil")
	}
	if p.TokenomicsParams == nil {
		return fmt.Errorf("tokenomics params cannot be nil")
	}
	if p.CollateralParams == nil {
		return fmt.Errorf("collateral params cannot be nil")
	}
	if p.BitcoinRewardParams == nil {
		return fmt.Errorf("bitcoin reward params cannot be nil")
	}
	if p.EpochParams == nil {
		return fmt.Errorf("epoch params cannot be nil")
	}
	if p.PocParams == nil {
		return fmt.Errorf("poc params cannot be nil")
	}
	if err := p.ValidationParams.Validate(); err != nil {
		return err
	}
	if err := p.TokenomicsParams.Validate(); err != nil {
		return err
	}
	if err := p.BitcoinRewardParams.Validate(); err != nil {
		return err
	}
	if err := p.EpochParams.Validate(); err != nil {
		return err
	}
	if err := p.CollateralParams.Validate(); err != nil {
		return err
	}
	if err := p.DynamicPricingParams.Validate(); err != nil {
		return err
	}

	if p.GenesisGuardianParams != nil {
		if p.GenesisGuardianParams.NetworkMaturityThreshold < 0 {
			return fmt.Errorf("genesis guardian network maturity threshold cannot be negative")
		}
		if p.GenesisGuardianParams.NetworkMaturityMinHeight < 0 {
			return fmt.Errorf("genesis guardian network maturity min height cannot be negative")
		}
	}

	if p.DeveloperAccessParams != nil {
		if p.DeveloperAccessParams.UntilBlockHeight < 0 {
			return fmt.Errorf("developer access until block height cannot be negative")
		}
	}

	if p.ParticipantAccessParams != nil {
		if p.ParticipantAccessParams.NewParticipantRegistrationStartHeight < 0 {
			return fmt.Errorf("new participant registration start height cannot be negative")
		}
		if p.ParticipantAccessParams.ParticipantAllowlistUntilBlockHeight < 0 {
			return fmt.Errorf("participant allowlist until block height cannot be negative")
		}
	}

	if p.PocParams != nil {
		if err := p.PocParams.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p *PocParams) Validate() error {
	if p == nil {
		return nil
	}
	if p.SeqLen < 0 {
		return fmt.Errorf("poc_params.seq_len cannot be negative")
	}
	return nil
}

func (p *ValidationParams) Validate() error {
	// Check for nil Decimal fields first
	if p.FalsePositiveRate == nil {
		return fmt.Errorf("false positive rate cannot be nil")
	}
	if p.PassValue == nil {
		return fmt.Errorf("pass value cannot be nil")
	}
	if p.MinValidationAverage == nil {
		return fmt.Errorf("min validation average cannot be nil")
	}
	if p.MaxValidationAverage == nil {
		return fmt.Errorf("max validation average cannot be nil")
	}
	if p.MinValidationHalfway == nil {
		return fmt.Errorf("min validation halfway cannot be nil")
	}
	if p.MissPercentageCutoff == nil {
		return fmt.Errorf("miss percentage cutoff cannot be nil")
	}
	if p.MissRequestsPenalty == nil {
		return fmt.Errorf("miss requests penalty cannot be nil")
	}
	// v0.2.5 parameters
	if p.BadParticipantInvalidationRate == nil {
		return fmt.Errorf("bad participant invalidation rate cannot be nil")
	}
	if p.InvalidationHThreshold == nil {
		return fmt.Errorf("invalidation h threshold cannot be nil")
	}
	if p.DowntimeGoodPercentage == nil {
		return fmt.Errorf("downtime good percentage cannot be nil")
	}
	if p.DowntimeBadPercentage == nil {
		return fmt.Errorf("downtime bad percentage cannot be nil")
	}
	if p.DowntimeHThreshold == nil {
		return fmt.Errorf("downtime h threshold cannot be nil")
	}
	if p.QuickFailureThreshold == nil {
		return fmt.Errorf("quick failure threshold cannot be nil")
	}
	if p.InvalidReputationPreserve == nil {
		return fmt.Errorf("invalid reputation preserve cannot be nil")
	}
	if p.DowntimeReputationPreserve == nil {
		return fmt.Errorf("downtime reputation preserve cannot be nil")
	}
	if p.BinomTestP0 == nil {
		return fmt.Errorf("binom test p0 cannot be nil")
	}
	// Validate timestamp parameters
	if p.TimestampExpiration <= 0 {
		return fmt.Errorf("timestamp expiration must be positive")
	}
	if p.TimestampAdvance <= 0 {
		return fmt.Errorf("timestamp advance must be positive")
	}
	return nil
}

func (p *TokenomicsParams) Validate() error {
	// Check for nil Decimal fields first
	if p.SubsidyReductionInterval == nil {
		return fmt.Errorf("subsidy reduction interval cannot be nil")
	}
	if p.SubsidyReductionAmount == nil {
		return fmt.Errorf("subsidy reduction amount cannot be nil")
	}
	if p.CurrentSubsidyPercentage == nil {
		return fmt.Errorf("current subsidy percentage cannot be nil")
	}
	if p.TopRewardAllowedFailure == nil {
		return fmt.Errorf("top reward allowed failure cannot be nil")
	}

	// Validate vesting parameters
	if err := validateVestingPeriod(p.WorkVestingPeriod); err != nil {
		return errors.Wrap(err, "invalid work_vesting_period")
	}
	if err := validateVestingPeriod(p.RewardVestingPeriod); err != nil {
		return errors.Wrap(err, "invalid reward_vesting_period")
	}
	if err := validateVestingPeriod(p.TopMinerVestingPeriod); err != nil {
		return errors.Wrap(err, "invalid top_miner_vesting_period")
	}

	return nil
}

func (p *CollateralParams) Validate() error {
	if err := validateSlashFraction(p.SlashFractionInvalid); err != nil {
		return errors.Wrap(err, "invalid slash_fraction_invalid")
	}
	if err := validateSlashFraction(p.SlashFractionDowntime); err != nil {
		return errors.Wrap(err, "invalid slash_fraction_downtime")
	}
	if err := validatePercentage(p.DowntimeMissedPercentageThreshold); err != nil {
		return errors.Wrap(err, "invalid downtime_missed_percentage_threshold")
	}
	if err := validateEpoch(p.GracePeriodEndEpoch); err != nil {
		return errors.Wrap(err, "invalid grace_period_end_epoch")
	}
	if err := validateBaseWeightRatio(p.BaseWeightRatio); err != nil {
		return errors.Wrap(err, "invalid base_weight_ratio")
	}
	if err := validateCollateralPerWeightUnit(p.CollateralPerWeightUnit); err != nil {
		return errors.Wrap(err, "invalid collateral_per_weight_unit")
	}
	return nil
}

func (p *BitcoinRewardParams) Validate() error {
	// Check for nil Decimal fields first
	if p.DecayRate == nil {
		return fmt.Errorf("decay rate cannot be nil")
	}
	if p.UtilizationBonusFactor == nil {
		return fmt.Errorf("utilization bonus factor cannot be nil")
	}
	if p.FullCoverageBonusFactor == nil {
		return fmt.Errorf("full coverage bonus factor cannot be nil")
	}
	if p.PartialCoverageBonusFactor == nil {
		return fmt.Errorf("partial coverage bonus factor cannot be nil")
	}

	// Validate parameters
	if err := validateInitialEpochReward(p.InitialEpochReward); err != nil {
		return errors.Wrap(err, "invalid initial_epoch_reward")
	}
	if err := validateDecayRate(p.DecayRate); err != nil {
		return errors.Wrap(err, "invalid decay_rate")
	}
	if err := validateEpoch(p.GenesisEpoch); err != nil {
		return errors.Wrap(err, "invalid genesis_epoch")
	}
	if err := validateBonusFactor(p.UtilizationBonusFactor); err != nil {
		return errors.Wrap(err, "invalid utilization_bonus_factor")
	}
	if err := validateBonusFactor(p.FullCoverageBonusFactor); err != nil {
		return errors.Wrap(err, "invalid full_coverage_bonus_factor")
	}
	if err := validateBonusFactor(p.PartialCoverageBonusFactor); err != nil {
		return errors.Wrap(err, "invalid partial_coverage_bonus_factor")
	}

	return nil
}

func (p *DynamicPricingParams) Validate() error {
	// Check for nil Decimal fields first
	if p.StabilityZoneLowerBound == nil {
		return fmt.Errorf("stability zone lower bound cannot be nil")
	}
	if p.StabilityZoneUpperBound == nil {
		return fmt.Errorf("stability zone upper bound cannot be nil")
	}
	if p.PriceElasticity == nil {
		return fmt.Errorf("price elasticity cannot be nil")
	}

	// Validate parameters
	if err := validateStabilityZoneBound(p.StabilityZoneLowerBound); err != nil {
		return errors.Wrap(err, "invalid stability_zone_lower_bound")
	}
	if err := validateStabilityZoneBound(p.StabilityZoneUpperBound); err != nil {
		return errors.Wrap(err, "invalid stability_zone_upper_bound")
	}
	if err := validatePriceElasticity(p.PriceElasticity); err != nil {
		return errors.Wrap(err, "invalid price_elasticity")
	}
	if err := validateUtilizationWindowDuration(p.UtilizationWindowDuration); err != nil {
		return errors.Wrap(err, "invalid utilization_window_duration")
	}
	if err := validatePerTokenPrice(p.MinPerTokenPrice); err != nil {
		return errors.Wrap(err, "invalid min_per_token_price")
	}
	if err := validatePerTokenPrice(p.BasePerTokenPrice); err != nil {
		return errors.Wrap(err, "invalid base_per_token_price")
	}
	if err := validateGracePeriodPerTokenPrice(p.GracePeriodPerTokenPrice); err != nil {
		return errors.Wrap(err, "invalid grace_period_per_token_price")
	}
	if err := validateEpoch(p.GracePeriodEndEpoch); err != nil {
		return errors.Wrap(err, "invalid grace_period_end_epoch")
	}

	// Validate stability zone bounds are logically consistent
	lowerBound := p.StabilityZoneLowerBound.ToDecimal()
	upperBound := p.StabilityZoneUpperBound.ToDecimal()
	if lowerBound.GreaterThanOrEqual(upperBound) {
		return fmt.Errorf("stability zone lower bound (%s) must be less than upper bound (%s)", lowerBound.String(), upperBound.String())
	}

	return nil
}

func validateSlashFraction(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() || legacyDec.GT(math.LegacyOneDec()) {
		return fmt.Errorf("slash fraction must be between 0 and 1, but is %s", legacyDec.String())
	}
	return nil
}

func validateBaseWeightRatio(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() {
		return fmt.Errorf("base weight ratio cannot be negative: %s", legacyDec)
	}

	if legacyDec.GT(math.LegacyOneDec()) {
		return fmt.Errorf("base weight ratio cannot be greater than 1: %s", legacyDec)
	}

	return nil
}

func validateCollateralPerWeightUnit(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() {
		return fmt.Errorf("collateral per weight unit cannot be negative: %s", legacyDec)
	}
	return nil
}

func validateVestingPeriod(i interface{}) error {
	if i == nil {
		return fmt.Errorf("vesting period cannot be nil")
	}

	switch v := i.(type) {
	case *uint64:
		// Pointer to uint64 (what we expect from ParamSetPairs)
		if v == nil {
			return fmt.Errorf("vesting period cannot be nil")
		}
		return nil
	case uint64:
		// Direct uint64 value (also valid)
		return nil
	default:
		return fmt.Errorf("invalid parameter type: %T", i)
	}
}

// ValidateVestingPeriod is the exported version of validateVestingPeriod for testing
func ValidateVestingPeriod(i interface{}) error {
	return validateVestingPeriod(i)
}

func validatePercentage(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() || legacyDec.GT(math.LegacyOneDec()) {
		return fmt.Errorf("percentage must be between 0 and 1, but is %s", legacyDec.String())
	}
	return nil
}

func validateEpoch(i interface{}) error {
	_, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	return nil
}

func validateInitialEpochReward(i interface{}) error {
	_, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	return nil
}

func validateDecayRate(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	// Decay rate should be negative for gradual reduction
	if legacyDec.IsPositive() {
		return fmt.Errorf("decay rate must be negative for reward reduction, but is %s", legacyDec.String())
	}
	// Reasonable bounds for decay rate (not too extreme)
	if legacyDec.LT(math.LegacyNewDecWithPrec(-1, 2)) { // Less than -0.01
		return fmt.Errorf("decay rate too extreme (less than -0.01): %s", legacyDec.String())
	}
	_, err = GetExponent(v.ToDecimal())
	if err != nil {
		return fmt.Errorf("decay rate does not have exponent defined %s", legacyDec.String())
	}
	return nil
}

func validateBonusFactor(i interface{}) error {
	v, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	legacyDec, err := v.ToLegacyDec()
	if err != nil {
		return err
	}
	if legacyDec.IsNegative() {
		return fmt.Errorf("bonus factor cannot be negative: %s", legacyDec.String())
	}
	return nil
}

func validateUseBitcoinRewards(i interface{}) error {
	_, ok := i.(bool)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	return nil
}

// Dynamic pricing validation functions
func validateStabilityZoneBound(i interface{}) error {
	bound, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if bound == nil {
		return fmt.Errorf("stability zone bound cannot be nil")
	}

	value := bound.ToDecimal()
	if value.IsNegative() || value.GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("stability zone bound must be between 0.0 and 1.0, got: %s", value.String())
	}
	return nil
}

func validatePriceElasticity(i interface{}) error {
	elasticity, ok := i.(*Decimal)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if elasticity == nil {
		return fmt.Errorf("price elasticity cannot be nil")
	}

	value := elasticity.ToDecimal()
	if value.LessThanOrEqual(decimal.Zero) || value.GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("price elasticity must be between 0.0 and 1.0, got: %s", value.String())
	}
	return nil
}

func validateUtilizationWindowDuration(i interface{}) error {
	duration, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if duration == 0 {
		return fmt.Errorf("utilization window duration must be greater than 0")
	}
	if duration > 3600 { // Max 1 hour
		return fmt.Errorf("utilization window duration must not exceed 3600 seconds (1 hour), got: %d", duration)
	}
	return nil
}

func validatePerTokenPrice(i interface{}) error {
	price, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if price == 0 {
		return fmt.Errorf("per-token price must be greater than 0")
	}
	return nil
}

func validateGracePeriodPerTokenPrice(i interface{}) error {
	_, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	// Grace period price can be 0 (free inference) or any positive value
	return nil
}

func validateSetNewValidatorsDelay(i interface{}) error {
	v, ok := i.(int64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if v < 0 {
		return fmt.Errorf("set new validators delay cannot be negative")
	}
	return nil
}

func validateInferenceValidationCutoff(i interface{}) error {
	v, ok := i.(int64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if v < 0 {
		return fmt.Errorf("inference validation cutoff cannot be negative")
	}
	return nil
}

func validateInferencePruningEpochThreshold(i interface{}) error {
	v, ok := i.(uint64)
	if !ok {
		return fmt.Errorf("invalid parameter type: %T", i)
	}
	if v < 1 {
		return fmt.Errorf("inference pruning epoch threshold must be at least 1")
	}
	return nil
}

func (d *Decimal) ToLegacyDec() (math.LegacyDec, error) {
	return math.LegacyNewDecFromStr(d.ToDecimal().String())
}

func (d *Decimal) ToDecimal() decimal.Decimal {
	return decimal.New(d.Value, d.Exponent)
}

func (d *Decimal) ToFloat() float64 {
	return d.ToDecimal().InexactFloat64()
}

func DecimalFromFloat(f float64) *Decimal {
	d := decimal.NewFromFloat(f)
	return &Decimal{Value: d.CoefficientInt64(), Exponent: d.Exponent()}
}

func DecimalFromDecimal(d decimal.Decimal) *Decimal {
	return &Decimal{Value: d.CoefficientInt64(), Exponent: d.Exponent()}
}

var DecimalZero = Decimal{Value: 0, Exponent: 0}

func DecimalFromFloat32(f float32) *Decimal {
	d := decimal.NewFromFloat32(f)
	return &Decimal{Value: d.CoefficientInt64(), Exponent: d.Exponent()}
}

func (p *PocParams) GetWeightScaleFactorDec() math.LegacyDec {
	if p.WeightScaleFactor == nil || (p.WeightScaleFactor.Value == 0 && p.WeightScaleFactor.Exponent == 0) {
		return math.LegacyOneDec()
	}
	dec, err := p.WeightScaleFactor.ToLegacyDec()
	if err != nil {
		return math.LegacyOneDec()
	}
	return dec
}

var (
	decayRate475       = decimal.New(-475, -6)
	exponent475        = decimal.New(9995251127946402, -16)
	decayRateVerySmall = decimal.New(-1, -6)
	exponentVerySmall  = decimal.New(9999990000005, -13)
	decayRatePositive  = decimal.New(1, -4)
	exponentPositive   = decimal.New(10001000050001667, -16)
	decayRateZero      = decimal.Zero
	exponentZero       = decimal.NewFromInt(1)
)

func GetExponent(decayRate decimal.Decimal) (decimal.Decimal, error) {
	if decayRate.Equal(decayRate475) {
		return exponent475, nil
	}
	// For testing only (not a problem for production)
	if decayRate.Equal(decayRateVerySmall) {
		return exponentVerySmall, nil
	}
	if decayRate.Equal(decayRatePositive) {
		return exponentPositive, nil
	}
	if decayRate.Equal(decayRateZero) {
		return exponentZero, nil
	}
	return decimal.Zero, fmt.Errorf("unsupported decay rate: %s", decayRate.String())
}
