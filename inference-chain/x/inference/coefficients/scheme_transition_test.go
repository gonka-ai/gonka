package coefficients

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestAdjustmentUsesPreviousEpochAndDilutionUsesCurrentEpoch(t *testing.T) {
	config := params(
		model("a", dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		model("b", dec(5, -1), dec(2, 0), dec(1, 0), 5000),
	)
	previous := []*types.ConfirmationWeightScale{
		state("a", dec(1, 0), dec(25, -3), 0),
		state("b", dec(1, 0), dec(25, -3), 0),
	}
	result, err := calculateForTest(config, previous,
		map[string]int64{"a": 100, "b": 900},
		map[string]int64{"a": 900, "b": 100}, nil, true)
	require.NoError(t, err)
	scales := stateMap(result.Scales)
	require.Equal(t, dec(105, -2), scales["a"].BaseCoefficient)
	require.EqualValues(t, 1, scales["a"].PrevSign)
	require.Equal(t, dec(95, -2), scales["b"].BaseCoefficient)
	require.EqualValues(t, -1, scales["b"].PrevSign)
	require.Equal(t, "0.805555555555555556", result.Effective["a"].String())
	require.Equal(t, "0.950000000000000000", result.Effective["b"].String())
}

func TestBaseOnlySeedResetsHistoryAndResumesAdjustment(t *testing.T) {
	config := params(
		model("a", dec(5, -1), dec(2, 0), dec(1, 0), 5000),
		model("b", dec(1, 0), dec(1, 0), dec(1, 0), 5000),
	)
	raw := map[string]int64{"a": 20, "b": 80}
	seed := []*types.ConfirmationWeightScale{{ModelId: "a", BaseCoefficient: dec(15, -1)}}
	first, err := calculateForTest(config, seed, map[string]int64{"a": 99, "b": 1}, raw, nil, false)
	require.NoError(t, err)
	a := stateMap(first.Scales)["a"]
	require.Equal(t, dec(15, -1), a.BaseCoefficient)
	require.Equal(t, dec(25, -3), a.AdaptiveStep)
	require.Zero(t, a.PrevSign)
	require.Equal(t, dec(1, 0), stateMap(first.Scales)["b"].BaseCoefficient)
	second, err := calculateForTest(config, first.Scales, raw, raw, nil, true)
	require.NoError(t, err)
	a = stateMap(second.Scales)["a"]
	require.Equal(t, dec(1575, -3), a.BaseCoefficient) // 1.5 * 1.05
	require.Equal(t, dec(5, -2), a.AdaptiveStep)
	require.EqualValues(t, 1, a.PrevSign)
}

// Raw model totals stay fixed across epochs in each synthetic scenario.
func TestSimulateSchemeCoefficientTransition(t *testing.T) {
	ids := []string{"fixed", "adaptive-a", "adaptive-b"}
	config := params(
		model(ids[0], dec(1, 0), dec(1, 0), dec(1, 0), 3334),
		model(ids[1], dec(5, -1), dec(2, 0), dec(2, 0), 3333),
		model(ids[2], dec(1, 0), dec(3, 0), dec(4, 0), 3333),
	)
	config.PocScheme = types.PocScheme_POC_SCHEME_DECODE
	for _, model := range config.Models {
		model.Schemes[0].Scheme = types.PocScheme_POC_SCHEME_DECODE
	}
	seeds := []*types.ConfirmationWeightScale{
		{ModelId: ids[0], BaseCoefficient: dec(1, 0)},
		{ModelId: ids[1], BaseCoefficient: dec(125, -2)},
		{ModelId: ids[2], BaseCoefficient: dec(2, 0)},
	}
	for _, scenario := range []struct {
		name string
		raw  map[string]int64
	}{
		{"balanced", map[string]int64{ids[0]: 100000, ids[1]: 50000, ids[2]: 25000}},
		{"adaptive-a under target", map[string]int64{ids[0]: 100000, ids[1]: 25000, ids[2]: 25000}},
		{"adaptive-a over target", map[string]int64{ids[0]: 100000, ids[1]: 100000, ids[2]: 25000}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			for _, mode := range []string{"reset to minimum", "carry base"} {
				var previous []*types.ConfirmationWeightScale
				if mode == "carry base" {
					previous = seeds
				}
				for epoch := 1; epoch <= 6; epoch++ {
					result, err := calculateForTest(config, previous, scenario.raw, scenario.raw, ids, epoch > 1)
					require.NoError(t, err)
					states := stateMap(result.Scales)
					for i, id := range ids {
						base, err := states[id].BaseCoefficient.ToLegacyDec()
						require.NoError(t, err)
						min, _ := config.Models[i].Schemes[0].DynamicCoefficient.CoeffMin.ToLegacyDec()
						max, _ := config.Models[i].Schemes[0].DynamicCoefficient.CoeffMax.ToLegacyDec()
						require.True(t, base.GTE(min) && base.LTE(max))
						if epoch == 1 && mode == "carry base" {
							for _, seed := range seeds {
								if seed.ModelId == id {
									require.Equal(t, seed.BaseCoefficient, states[id].BaseCoefficient)
								}
							}
						}
					}
					require.Equal(t, dec(1, 0), states[ids[0]].BaseCoefficient)
					if mode == "carry base" {
						ds, _ := states[ids[1]].BaseCoefficient.ToLegacyDec()
						initial, _ := seeds[1].BaseCoefficient.ToLegacyDec()
						switch scenario.name {
						case "balanced":
							require.True(t, ds.Equal(initial))
						case "adaptive-a under target":
							if epoch > 1 {
								require.True(t, ds.GT(initial))
							}
						case "adaptive-a over target":
							if epoch > 1 {
								require.True(t, ds.LT(initial))
							}
						}
					}
					t.Logf("%s epoch=%d adaptive-a base=%s effective=%s adaptive-b base=%s effective=%s", mode, epoch,
						decimalString(states[ids[1]].BaseCoefficient), result.Effective[ids[1]],
						decimalString(states[ids[2]].BaseCoefficient), result.Effective[ids[2]])
					previous = result.Scales
				}
			}
		})
	}
}

func decimalString(d *types.Decimal) string {
	v, err := d.ToLegacyDec()
	if err != nil {
		panic(err)
	}
	return v.String()
}
