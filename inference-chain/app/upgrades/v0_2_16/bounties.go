package v0_2_16

import (
	"context"
	"encoding/json"
	"strconv"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/keeper"
)

const BountyCommunitySaleContractAddress = "gonka18pkq9mwxxlmyq7kr5txhm060wemg2s4u94wvsfd9w2kdc0u99d6spk8pz2"
const BountyIbcUsdtDenom = "ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4"

func USDT(amount int64) int64 {
	return amount * 1_000_000
}

type BountyReward struct {
	Address string
	Amount  int64
}

// Bounties from "bounty v0.2.16 - Sheet3.csv". Amounts are in USDT.
// Keep one payment per spreadsheet row, including repeated recipients.
var bountyRewards = []BountyReward{
	// ML-node observability
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// Development 4,000: ML-node metrics exporter, DAPI federation endpoint, Grafana dashboards, public repo
	// [gonka-grafana](https://github.com/kaitakuai/gonka-grafana)
	// Hosting and maintenance 8,000: [monitoring.kaitaku.ai](https://monitoring.kaitaku.ai), with a
	// commitment to continue.
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-mlnode-observability.md)
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(12000)},

	// ML-node observability
	// Recipient: @qdanik
	// Contribution to the same ML-node observability track.
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-mlnode-observability.md)
	{Address: "gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8", Amount: USDT(500)},

	// decode-PoC experiments
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// vllm port and experiments with reevaluation on all hardware schemes and models
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-06-decode-poc-research-and-integration.md)
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(20000)},

	// decode-PoC, CUDA graph support
	// Recipient: Axel-t
	// CUDA Graph Support for Decode PoC
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-06-decode-poc-research-and-integration.md)
	{Address: "gonka1yhdhp4vwsvdsplv4acksntx0zxh8saueq6lj9m", Amount: USDT(18000)},

	// gonka-poc as plugin
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// Plugin removing re-porting PoC on every vLLM release.
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-gonka-poc-plugin-and-residual.md)
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(5000)},

	// DeepSeek-V4-Flash
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// Experiments and new model proposal.
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-07-deepseek-v4-flash-integration.md)
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(5000)},

	// GLM-5.3-Flash
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// Experiments and new model proposal.
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-09-glm-5-3-flash-integration.md)
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(4000)},

	// GLM-5.2
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// Experiments, images for B300/B200/H200.
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-06-glm-5-2-bring-up.md)
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(2000)},

	// Hy3 evaluation
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// Experiments.
	// [Report](https://github.com/kaitakuai/experiments/blob/main/reports/2026-08-hy3-evaluation.md)
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(2000)},

	// GPU rental reimbursement for model and PoC experiments
	// Recipient: kaitaku.ai (@baychak, @clanster)
	// Rented GPUs for the experiments above since 2026-06-01 (Verda, Vast, Dataoorts, Nebius), at cost
	// against billing
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(9600)},

	// Release management
	// Recipient: @x0152
	// PR reviews, HackerOne report reviews, ongoing training contributions Trainshards v0
	// ([#1350](https://github.com/gonka-ai/gonka/pull/1350),
	// [#1618](https://github.com/gonka-ai/gonka/pull/1618))
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: USDT(8000)},

	// Chain-halt bug report #1205
	// Recipient: @vitaly-andr
	// Bug report: https://github.com/gonka-ai/gonka/issues/1205
	// Fix: https://github.com/gonka-ai/cosmos-sdk/pull/16
	{Address: "gonka1uqt4hue8tljwwgdkvtthyl3n8kkkqtydyns4cm", Amount: USDT(5000)},

	// v0.2.16 review
	// Recipient: @vitaly-andr
	// upgrade review
	{Address: "gonka1uqt4hue8tljwwgdkvtthyl3n8kkkqtydyns4cm", Amount: USDT(1000)},

	// v0.2.16 review
	// Recipient: @bonujel
	// upgrade review
	{Address: "gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57", Amount: USDT(1000)},

	// HackerOne reviews
	// Recipient: @staaason / @zpoken
	// HackerOne report reviews plus [#1767](https://github.com/gonka-ai/gonka/pull/1767) and
	// [#1552](https://github.com/gonka-ai/gonka/pull/1552) (landed as
	// [#1623](https://github.com/gonka-ai/gonka/pull/1623))
	{Address: "gonka1s8zggm642e3kncy48c7vxwmeclt2wxyyd8qtdt", Amount: USDT(8050)},

	// v0.2.16 review and fixes
	// Recipient: @cyberdelamain
	// upgrade review and PRs [#1828](https://github.com/gonka-ai/gonka/pull/1828),
	// [#1846](https://github.com/gonka-ai/gonka/pull/1846),
	// [#1845](https://github.com/gonka-ai/gonka/pull/1845),
	// [#1859](https://github.com/gonka-ai/gonka/pull/1859)
	{Address: "gonka15u0r3mf6t7zsfuslusnyt7hsjrq357yumpe8st", Amount: USDT(3000)},
}

func distributeBountyRewards(ctx context.Context, k keeper.Keeper) error {
	if len(bountyRewards) == 0 {
		k.Logger().Info("No bounty rewards to distribute")
		return nil
	}

	communitySaleAddr, err := sdk.AccAddressFromBech32(BountyCommunitySaleContractAddress)
	if err != nil {
		k.Logger().Error("invalid hardcoded community sale contract address", "address", BountyCommunitySaleContractAddress, "error", err)
		return nil
	}
	authorityAddr, err := sdk.AccAddressFromBech32(k.GetAuthority())
	if err != nil {
		k.Logger().Error("invalid authority address", "authority", k.GetAuthority(), "error", err)
		return nil
	}

	var totalRequired int64
	for _, bounty := range bountyRewards {
		totalRequired += bounty.Amount
	}

	available := k.BankView.SpendableCoin(ctx, communitySaleAddr, BountyIbcUsdtDenom).Amount.Int64()
	if available < totalRequired {
		k.Logger().Warn("insufficient community sale balance, skipping bounty distribution",
			"required", totalRequired, "available", available, "denom", BountyIbcUsdtDenom)
		return nil
	}

	k.Logger().Info("community sale balance sufficient for bounty distribution",
		"required", totalRequired, "available", available, "denom", BountyIbcUsdtDenom)

	permissionedKeeper := wasmkeeper.NewGovPermissionKeeper(k.GetWasmKeeper())
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, bounty := range bountyRewards {
		recipient, err := sdk.AccAddressFromBech32(bounty.Address)
		if err != nil {
			k.Logger().Error("invalid bounty address", "address", bounty.Address, "error", err)
			continue
		}

		msgBz, err := json.Marshal(map[string]any{
			"withdraw_ibc": map[string]string{
				"denom":     BountyIbcUsdtDenom,
				"amount":    strconv.FormatInt(bounty.Amount, 10),
				"recipient": recipient.String(),
			},
		})
		if err != nil {
			k.Logger().Error("failed to marshal community sale withdraw message", "address", bounty.Address, "error", err)
			continue
		}

		if _, err := permissionedKeeper.Execute(sdkCtx, communitySaleAddr, authorityAddr, msgBz, sdk.NewCoins()); err != nil {
			k.Logger().Error("failed to distribute bounty from community sale contract",
				"address", bounty.Address, "amount", bounty.Amount, "denom", BountyIbcUsdtDenom, "error", err)
			continue
		}

		k.Logger().Info("bounty distributed from community sale contract",
			"address", bounty.Address, "amount", bounty.Amount, "denom", BountyIbcUsdtDenom)
	}

	return nil
}
