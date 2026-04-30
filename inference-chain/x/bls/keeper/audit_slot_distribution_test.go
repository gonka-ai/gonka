package keeper_test

import (
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/math"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/types"
)

var realWeights = []int64{
	95940, 93537, 81132, 63671, 47066, 36876, 33458, 31502, 29772, 28219,
	26091, 22791, 20930, 20835, 20078, 19209, 18674, 15246, 15047, 14284,
	13303, 12421, 12408, 11908, 11885, 11560, 10675, 10610, 10543, 10460,
	10356, 10341, 10262, 9811, 9270, 8565, 8565, 8506, 8490, 8423,
	8355, 8282, 8154, 7950, 7343, 7180, 5795, 4344, 4238, 4185,
	4177, 4116, 4027, 3156, 2945, 2808, 2563, 2563, 2563, 2563,
	2522, 2405, 2167, 2158, 2158, 2156, 2148, 2148, 2146, 2138,
	2138, 2129, 2128, 2126, 2126, 2120, 2114, 2109, 2101, 2101,
	2099, 2095, 2095, 2095, 2092, 2092, 2092, 2085, 2085, 2082,
	2082, 2073, 2073, 2073, 2063, 2063, 2051, 2045, 2035, 2011,
	2011, 1953, 1926, 1919, 1654, 1637, 1564, 1545, 1035, 989,
	984, 933, 848, 811, 609, 601, 579, 537, 420, 362,
	177, 91, 82,
}

func makeParticipantsAudit(weights []int64) []types.ParticipantWithWeightAndKey {
	p := make([]types.ParticipantWithWeightAndKey, len(weights))
	for i, w := range weights {
		p[i] = types.ParticipantWithWeightAndKey{
			Address:            fmt.Sprintf("cosmos1val%03d", i+1),
			PercentageWeight:   math.LegacyNewDec(w),
			Secp256k1PublicKey: []byte(fmt.Sprintf("key%03d", i+1)),
		}
	}
	return p
}

func makeSybils(count int, weightEach int64) []types.ParticipantWithWeightAndKey {
	p := make([]types.ParticipantWithWeightAndKey, count)
	for i := 0; i < count; i++ {
		p[i] = types.ParticipantWithWeightAndKey{
			Address:            fmt.Sprintf("cosmos1atk%04d", i+1),
			PercentageWeight:   math.LegacyNewDec(weightEach),
			Secp256k1PublicKey: []byte(fmt.Sprintf("atk_key%04d", i+1)),
		}
	}
	return p
}

func countSlots(result []types.BLSParticipantInfo) (atkSlots, honestSlots int) {
	for _, p := range result {
		sc := int(p.SlotEndIndex-p.SlotStartIndex) + 1
		if strings.HasPrefix(p.Address, "cosmos1atk") {
			atkSlots += sc
		} else {
			honestSlots += sc
		}
	}
	return
}

func sumW(w []int64) int64 {
	t := int64(0)
	for _, v := range w {
		t += v
	}
	return t
}

func TestBLS_26Signers_100Slots_Baseline(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	top26 := realWeights[:26]
	result, err := k.AssignSlots(ctx, makeParticipantsAudit(top26), 100)
	if err != nil {
		t.Fatal(err)
	}

	selectedWeight := sumW(top26)

	fmt.Printf("\n========================================================================\n")
	fmt.Printf("BASELINE: 26 honest signers, 100 slots (t=50, sign=51, block=50)\n")
	fmt.Printf("========================================================================\n\n")
	fmt.Printf("%-16s %8s %7s %6s\n", "Address", "Weight", "Wt%", "Slots")
	fmt.Printf("%s\n", strings.Repeat("-", 42))

	for _, p := range result {
		sc := p.SlotEndIndex - p.SlotStartIndex + 1
		w := p.PercentageWeight.TruncateInt64()
		pct := float64(w) / float64(selectedWeight) * 100
		fmt.Printf("%-16s %8d %6.2f%% %6d\n", p.Address, w, pct, sc)
	}
}

func TestBLS_SybilAttack_Full(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)

	totalSlots := uint32(100)
	signThreshold := 51
	blockThreshold := 50

	fmt.Printf("\n========================================================================\n")
	fmt.Printf("BLS SYBIL ATTACK ANALYSIS\n")
	fmt.Printf("========================================================================\n")
	fmt.Printf("iTotalSlots=100, tSlotsDegree=50\n")
	fmt.Printf("  SIGN (forge): need %d+ slots   BLOCK: need %d+ slots\n\n", signThreshold, blockThreshold)

	scenarios := []struct {
		name          string
		honestWeights []int64
	}{
		{"26 honest signers", realWeights[:26]},
		{"123 honest validators", realWeights},
	}

	for _, sc := range scenarios {
		honestTotal := sumW(sc.honestWeights)
		nHonest := len(sc.honestWeights)

		fmt.Printf("\n========================================================================\n")
		fmt.Printf("%s (honest weight: %d)\n", sc.name, honestTotal)
		fmt.Printf("========================================================================\n\n")

		for _, atkFrac := range []float64{0.20, 0.30, 0.40, 0.49} {
			atkWeight := int64(float64(honestTotal) * atkFrac / (1 - atkFrac))

			fmt.Printf("--- f=%.0f%% (attacker weight %d, honest weight %d) ---\n",
				atkFrac*100, atkWeight, honestTotal)

			fmt.Printf("  %-8s %-12s %-8s %-8s %-8s %s\n",
				"Split", "Wt/identity", "TotalP", "AtkSlot", "HonSlot", "Result")
			fmt.Printf("  %s\n", strings.Repeat("-", 66))

			for _, nSybils := range []int{1, 2, 5, 10, 20, 30, 40, 50, 60, 70, 80} {
				perSybil := atkWeight / int64(nSybils)
				if perSybil <= 0 {
					break
				}

				honest := makeParticipantsAudit(sc.honestWeights)
				sybils := makeSybils(nSybils, perSybil)
				all := append(honest, sybils...)

				result, err := k.AssignSlots(ctx, all, totalSlots)
				if err != nil {
					fmt.Printf("  %-8d %-12d %-8d ERROR: %v\n", nSybils, perSybil, nHonest+nSybils, err)
					continue
				}

				atk, hon := countSlots(result)
				totalP := nHonest + nSybils
				status := ""
				if atk >= signThreshold {
					status = "FORGES"
				} else if atk >= blockThreshold {
					status = "BLOCKS"
				}
				fmt.Printf("  %-8d %-12d %-8d %-8d %-8d %s\n",
					nSybils, perSybil, totalP, atk, hon, status)
			}
			fmt.Println()
		}
	}

	// Summary table
	fmt.Printf("\n========================================================================\n")
	fmt.Printf("SUMMARY: Minimum attacker %% of total weight to achieve goal via Sybil\n")
	fmt.Printf("========================================================================\n\n")
	fmt.Printf("%-24s %-14s %-14s %-14s\n", "Scenario", "Block (50)", "Forge (51)", "Proportional")
	fmt.Printf("%s\n", strings.Repeat("-", 66))

	for _, sc := range scenarios {
		honestTotal := sumW(sc.honestWeights)

		for _, target := range []struct {
			name  string
			slots int
		}{
			{"Block", blockThreshold},
			{"Forge", signThreshold},
		} {
			minWeight := int64(0)
			for atkWeight := int64(1000); atkWeight <= honestTotal*2; atkWeight += 500 {
				found := false
				for nSybils := 1; nSybils <= 100; nSybils++ {
					perSybil := atkWeight / int64(nSybils)
					if perSybil <= 0 {
						break
					}
					honest := makeParticipantsAudit(sc.honestWeights)
					sybils := makeSybils(nSybils, perSybil)
					all := append(honest, sybils...)
					result, err := k.AssignSlots(ctx, all, totalSlots)
					if err != nil {
						continue
					}
					atk, _ := countSlots(result)
					if atk >= target.slots {
						found = true
						break
					}
				}
				if found {
					minWeight = atkWeight
					break
				}
			}

			if minWeight > 0 && target.name == "Block" {
				pctTotal := float64(minWeight) / float64(honestTotal+minWeight) * 100
				fmt.Printf("%-24s %.1f%%", sc.name, pctTotal)
			} else if minWeight > 0 && target.name == "Forge" {
				pctTotal := float64(minWeight) / float64(honestTotal+minWeight) * 100
				fmt.Printf("            %.1f%%", pctTotal)
			}
		}
		propBlock := float64(50)
		fmt.Printf("            %.0f%% / %.0f%%\n", propBlock, propBlock+1)
	}
}
