package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"

	mathsdk "cosmossdk.io/math"
	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

type config struct {
	Name       string                      `json:"name"`
	BaseModel  string                      `json:"base_model"`
	Epsilon    string                      `json:"epsilon"`
	MaxPasses  int                         `json:"max_passes"`
	Hosts      int                         `json:"hosts"`
	GPUCounts  map[string]int              `json:"gpu_counts"`
	Seed       int64                       `json:"seed"`
	Epochs     int                         `json:"epochs"`
	Controller controller                  `json:"controller"`
	Models     []model                     `json:"models"`
	Throughput map[string]map[string]int64 `json:"throughput"`
}

type controller struct {
	TargetZoneBPS     uint32 `json:"target_zone_bps"`
	StepMin           string `json:"step_min"`
	StepMax           string `json:"step_max"`
	BootstrapStepMax  string `json:"bootstrap_step_max"`
	BootstrapShareBPS uint32 `json:"bootstrap_share_bps"`
}

type model struct {
	ID             string `json:"id"`
	Min            string `json:"coeff_min"`
	Max            string `json:"coeff_max"`
	Difficulty     string `json:"relative_difficulty"`
	TargetShareBPS uint32 `json:"target_share_bps"`
}

type epoch struct {
	N         int                `json:"n"`
	Shares    map[string]float64 `json:"shares"`
	Base      map[string]float64 `json:"base_coefficients"`
	Effective map[string]float64 `json:"effective_coefficients"`
	GPUReward map[string]float64 `json:"gpu_reward_relative_to_8xH100"`
	Passes    int                `json:"allocation_passes"`
}

type report struct {
	Config   config     `json:"config"`
	Hardware [][]string `json:"hardware"`
	Epochs   []epoch    `json:"epochs"`
}

func main() {
	configPath := flag.String("config", "x/inference/coefficients/sim/config.json", "experiment config")
	outputDir := flag.String("output", "x/inference/coefficients/sim/output", "artifact directory")
	flag.Parse()

	cfg := loadConfig(*configPath)
	params, modelIDs := buildParams(cfg)
	frozen, err := coefficient.Freeze(params)
	check(err)
	rng := rand.New(rand.NewSource(cfg.Seed))
	hardware := buildHardware(cfg, rng)
	allocation := initialAllocation(hardware, cfg.BaseModel)
	epsilon := mustDec(cfg.Epsilon)

	var previous []*types.ConfirmationWeightScale
	var previousRaw map[string]int64
	epochs := make([]epoch, 0, cfg.Epochs+1)
	for n := 0; n <= cfg.Epochs; n++ {
		transition, err := coefficient.Calculate(
			frozen.Params, frozen.Scales, previous,
			previousRaw, nil, modelIDs, previous != nil,
		)
		check(err)
		passes := stabilize(
			cfg, modelIDs, hardware, allocation,
			frozen.Params, transition.Scales, epsilon, rng,
		)
		raw := rawTotals(cfg, modelIDs, hardware, allocation)
		effective, err := coefficient.EffectiveForAllocation(
			frozen.Params, transition.Scales, raw, modelIDs,
		)
		check(err)
		epochs = append(epochs, snapshotEpoch(
			n, cfg, modelIDs, raw, transition.Scales, effective, passes,
		))
		previous, previousRaw = transition.Scales, raw
	}

	check(os.MkdirAll(*outputDir, 0o755))
	check(writeJSON(filepath.Join(*outputDir, "experiment.json"), report{
		Config: cfg, Hardware: hardware, Epochs: epochs,
	}))
	check(writeSVG(filepath.Join(*outputDir, "experiment.svg"), cfg, modelIDs, epochs))
	fmt.Println(filepath.Join(*outputDir, "experiment.json"))
	fmt.Println(filepath.Join(*outputDir, "experiment.svg"))
}

func loadConfig(path string) config {
	data, err := os.ReadFile(path)
	check(err)
	var cfg config
	check(json.Unmarshal(data, &cfg))
	if cfg.Hosts <= 0 || cfg.Epochs < 0 || cfg.MaxPasses <= 0 {
		panic("hosts and max_passes must be positive; epochs must be non-negative")
	}
	return cfg
}

func buildParams(cfg config) (*types.PocParams, []string) {
	params := &types.PocParams{
		DynamicCoefficientParams: &types.DynamicCoefficientParams{
			TargetZoneBps:     cfg.Controller.TargetZoneBPS,
			StepMin:           protoDec(cfg.Controller.StepMin),
			StepMax:           protoDec(cfg.Controller.StepMax),
			BootstrapStepMax:  protoDec(cfg.Controller.BootstrapStepMax),
			BootstrapShareBps: cfg.Controller.BootstrapShareBPS,
		},
	}
	var modelIDs []string
	for _, model := range cfg.Models {
		params.Models = append(params.Models, &types.PoCModelConfig{
			ModelId: model.ID,
			DynamicCoefficient: &types.DynamicCoefficientModelConfig{
				CoeffMin:           protoDec(model.Min),
				CoeffMax:           protoDec(model.Max),
				RelativeDifficulty: protoDec(model.Difficulty),
				TargetShareBps:     model.TargetShareBPS,
			},
		})
		modelIDs = append(modelIDs, model.ID)
	}
	check(params.Validate())
	sort.Strings(modelIDs)
	return params, modelIDs
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func protoDec(value string) *types.Decimal {
	parsed, err := decimal.NewFromString(value)
	check(err)
	coefficient := parsed.Coefficient()
	if !coefficient.IsInt64() {
		panic(value + " exceeds Decimal int64 storage")
	}
	return &types.Decimal{Value: coefficient.Int64(), Exponent: parsed.Exponent()}
}

func mustDec(value string) mathsdk.LegacyDec {
	return mathsdk.LegacyMustNewDecFromStr(value)
}

func sortedKeys[V any](values map[string]V) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
