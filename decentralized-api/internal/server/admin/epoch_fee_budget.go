package admin

import (
	"net/http"
	"strconv"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/labstack/echo/v4"

	"decentralized-api/cosmosclient/tx_manager"

	"github.com/productscience/inference/x/inference/types"
)

const (
	countSourceParam          = "param"
	countSourceTopParticipant = "top_participant"
	countSourceNone           = "none"

	// Mirrors tx_manager.gasStoreCommitIntrinsic. Budget is an upper bound.
	budgetStoreCommitIntrinsic = uint64(200_000)
	// Observed HardwareDiff gasUsed on testnet with a working Simulate.
	typicalHardwareDiffGas = uint64(80_000)
)

type epochFeeBudgetResponse struct {
	Denom                 string `json:"denom"`
	SpendableBalance      string `json:"spendable_balance"`
	BudgetBalance         string `json:"budget_balance"`
	Count                 uint64 `json:"count"`
	CountSource           string `json:"count_source"`
	BudgetKnown           bool   `json:"budget_known"`
	SpendableCoversBudget bool   `json:"spendable_covers_budget"`
}

func (s *Server) getEpochFeeBudget(c echo.Context) error {
	ctx := c.Request().Context()

	count := uint64(0)
	countSource := countSourceNone
	if raw := c.QueryParam("count"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "count must be a non-negative integer")
		}
		count = parsed
		countSource = countSourceParam
	}

	queryClient := s.recorder.NewInferenceQueryClient()
	epochInfo, err := queryClient.EpochInfo(ctx, &types.QueryEpochInfoRequest{})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "query EpochInfo: "+err.Error())
	}

	params := epochInfo.GetParams()
	fp := params.FeeParams
	ep := params.EpochParams
	cp := params.ConfirmationPocParams

	if countSource != countSourceParam {
		stage := epochInfo.GetLatestEpoch().PocStartBlockHeight
		if stage > 0 {
			commits, err := queryClient.AllPoCV2StoreCommitsForStage(ctx, &types.QueryAllPoCV2StoreCommitsForStageRequest{
				PocStageStartBlockHeight: stage,
			})
			if err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, "query StoreCommits: "+err.Error())
			}
			if top := topParticipantCount(commits.GetCommits()); top > 0 {
				count = uint64(top)
				countSource = countSourceTopParticipant
			}
		}
	}

	budget := epochFeeBudgetNgonka(fp, ep, cp, count)

	payer := s.recorder.GetAccountAddress()
	signer := s.recorder.GetSignerAddress()
	spendable, err := tx_manager.FeePayerSpendable(
		ctx,
		s.recorder.GetClientContext(),
		payer,
		signer,
		time.Now(),
		s.recorder.BankBalances,
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "query spendable: "+err.Error())
	}

	budgetKnown := epochBudgetKnown(fp, countSource)
	return c.JSON(http.StatusOK, epochFeeBudgetResponse{
		Denom:                 types.BaseCoin,
		SpendableBalance:      spendable.String(),
		BudgetBalance:         budget.String(),
		Count:                 count,
		CountSource:           countSource,
		BudgetKnown:           budgetKnown,
		SpendableCoversBudget: budgetKnown && spendable.GTE(budget),
	})
}

func topParticipantCount(commits []*types.PoCV2StoreCommitWithAddress) uint32 {
	if len(commits) == 0 {
		return 0
	}
	sums := make(map[string]uint64, len(commits))
	var max uint64
	for _, c := range commits {
		if c == nil {
			continue
		}
		sums[c.ParticipantAddress] += uint64(c.Count)
		if sums[c.ParticipantAddress] > max {
			max = sums[c.ParticipantAddress]
		}
	}
	if max > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(max)
}

func epochPrice(fp *types.FeeParams) int64 {
	if fp == nil || !fp.IsGroupEnabled(types.FeeGroupEpoch) {
		return 0
	}
	g := fp.GroupByName(types.FeeGroupEpoch)
	if g == nil {
		return 0
	}
	return int64(g.MinGasPrice)
}

// epochBudgetKnown is true when count cannot change the budget: fees off,
// StoreCommit extra-rate is 0, or count was supplied / observed.
func epochBudgetKnown(fp *types.FeeParams, countSource string) bool {
	if epochPrice(fp) == 0 {
		return true
	}
	_, rate := storeCommitRates(fp)
	return rate == 0 || countSource != countSourceNone
}

func storeCommitRates(fp *types.FeeParams) (base, rate uint64) {
	if fp == nil {
		return 0, 0
	}
	group, rule := fp.RuleForTypeURL(sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))
	if pb := types.ResolvedPeriodBase(group, rule); pb != nil {
		base = pb.Gas
	}
	if rule != nil {
		if d := rule.GetStoredDelta(); d != nil {
			rate = d.GasPerUnit
		}
	}
	return base, rate
}

// maxConfirmationPocs is how many sequential CPoCs can start in one epoch's
// trigger window. ExpectedConfirmationsPerEpoch is a mean, not a cap.
func maxConfirmationPocs(ep *types.EpochParams, cp *types.ConfirmationPoCParams) uint64 {
	if ep == nil || cp == nil || cp.ExpectedConfirmationsPerEpoch == 0 {
		return 0
	}
	confirmationWindow := ep.PocStageDuration +
		ep.PocExchangeDuration +
		ep.PocValidationDelay +
		ep.PocValidationDuration +
		ep.SetNewValidatorsDelay +
		ep.ConfirmationPocSafetyWindow
	ec := types.EpochContext{
		EpochIndex:          2,
		PocStartBlockHeight: ep.EpochLength,
		EpochParams:         *ep,
	}
	triggerWindowEnd := ec.NextPoCStart() - ep.InferenceValidationCutoff - confirmationWindow
	triggerWindowLength := triggerWindowEnd - ec.SetNewValidators() + 1
	if triggerWindowLength <= 0 {
		return 0
	}
	grace := ep.InferenceValidationCutoff
	if grace < 1 {
		grace = 1
	}
	occupied := grace + confirmationWindow
	if occupied <= 0 {
		return 1
	}
	n := triggerWindowLength / occupied
	if n < 1 {
		return 1
	}
	return uint64(n)
}

func commitHeightsPerStage(ep *types.EpochParams) uint64 {
	if ep == nil {
		return 1
	}
	n := ep.PocStageDuration + ep.PocExchangeDuration
	if n < 1 {
		return 1
	}
	return uint64(n)
}

func hardwareDiffCount(ep *types.EpochParams) uint64 {
	if ep == nil || ep.EpochLength < 60 {
		return 1
	}
	return uint64(ep.EpochLength / 60)
}

func budgetHeadroom(v uint64) uint64 {
	if v > ^uint64(0)-v/5 {
		return ^uint64(0)
	}
	return v + v/5
}

func epochFeeBudgetNgonka(fp *types.FeeParams, ep *types.EpochParams, cp *types.ConfirmationPoCParams, count uint64) math.Int {
	price := epochPrice(fp)
	if price <= 0 {
		return math.ZeroInt()
	}
	base, rate := storeCommitRates(fp)
	stages := 1 + maxConfirmationPocs(ep, cp)
	nCommits := commitHeightsPerStage(ep)
	// DAPI pays (intrinsic + extra)×1.2 per StoreCommit. Deltas sum to count;
	// period base once per stage.
	perStageExtra := base + rate*count
	intrinsicPad := budgetHeadroom(budgetStoreCommitIntrinsic)
	commitGas := stages * (budgetHeadroom(perStageExtra) + nCommits*intrinsicPad)
	hdGas := hardwareDiffCount(ep) * budgetHeadroom(typicalHardwareDiffGas)
	totalGas := commitGas + hdGas
	return math.NewIntFromUint64(totalGas).MulRaw(price)
}
