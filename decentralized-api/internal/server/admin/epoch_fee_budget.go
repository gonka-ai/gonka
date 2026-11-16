package admin

import (
	"net/http"
	"sort"
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
	ServingModels         uint64 `json:"serving_models"`
	ServingModelsSource   string `json:"serving_models_source"`
	BudgetKnown           bool   `json:"budget_known"`
	SpendableCoversBudget bool   `json:"spendable_covers_budget"`
}

func (s *Server) getEpochFeeBudget(c echo.Context) error {
	ctx := c.Request().Context()

	count := uint64(0)
	var servingModels uint64
	countSource := countSourceNone
	modelsSource := countSourceNone
	if raw := c.QueryParam("count"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "count must be a non-negative integer")
		}
		count = parsed
		countSource = countSourceParam
	}
	if raw := c.QueryParam("serving_models"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "serving_models must be a non-negative integer")
		}
		servingModels = parsed
		modelsSource = countSourceParam
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

	if countSource == countSourceNone && modelsSource == countSourceNone {
		stage := epochInfo.GetLatestEpoch().PocStartBlockHeight
		if stage > 0 {
			commits, err := queryClient.AllPoCV2StoreCommitsForStage(ctx, &types.QueryAllPoCV2StoreCommitsForStageRequest{
				PocStageStartBlockHeight: stage,
			})
			if err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, "query StoreCommits: "+err.Error())
			}
			base, rate, perNew := storeCommitRates(fp)
			if topCount, models, ok := topParticipantStats(commits.GetCommits(), base, rate, perNew); ok {
				count = topCount
				servingModels = models
				countSource = countSourceTopParticipant
				modelsSource = countSourceTopParticipant
			}
		}
	}

	budget := epochFeeBudgetNgonka(fp, ep, cp, count, servingModels)

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

	budgetKnown := epochBudgetKnown(fp, countSource, modelsSource)
	return c.JSON(http.StatusOK, epochFeeBudgetResponse{
		Denom:                 types.BaseCoin,
		SpendableBalance:      spendable.String(),
		BudgetBalance:         budget.String(),
		Count:                 count,
		CountSource:           countSource,
		ServingModels:         servingModels,
		ServingModelsSource:   modelsSource,
		BudgetKnown:           budgetKnown,
		SpendableCoversBudget: budgetKnown && spendable.GTE(budget),
	})
}

// topParticipantStats picks the participant whose repeat of this stage costs
// the most: period base + rate×count + static gas×distinct models. Equal
// fees keep the lexicographically first address. One row is one model;
// repeated model ids keep the larger count.
func topParticipantStats(commits []*types.PoCV2StoreCommitWithAddress, base, rate, perNew uint64) (count uint64, models uint64, ok bool) {
	rows := make([]*types.PoCV2StoreCommitWithAddress, 0, len(commits))
	for _, c := range commits {
		if c != nil && c.ParticipantAddress != "" {
			rows = append(rows, c)
		}
	}
	if len(rows) == 0 {
		return 0, 0, false
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ParticipantAddress != rows[j].ParticipantAddress {
			return rows[i].ParticipantAddress < rows[j].ParticipantAddress
		}
		return rows[i].ModelId < rows[j].ModelId
	})
	var bestFee uint64
	chosen := false
	for i := 0; i < len(rows); {
		addr := rows[i].ParticipantAddress
		var sum uint64
		var nModels uint64
		for i < len(rows) && rows[i].ParticipantAddress == addr {
			model := rows[i].ModelId
			var maxCount uint32
			for i < len(rows) && rows[i].ParticipantAddress == addr && rows[i].ModelId == model {
				if rows[i].Count > maxCount {
					maxCount = rows[i].Count
				}
				i++
			}
			sum = saturatingAdd(sum, uint64(maxCount))
			if model != "" {
				nModels++
			}
		}
		fee := saturatingAdd(base, saturatingAdd(saturatingMul(rate, sum), saturatingMul(perNew, nModels)))
		if !chosen || fee > bestFee {
			chosen = true
			bestFee = fee
			count = sum
			models = nModels
		}
	}
	return count, models, chosen && (count > 0 || models > 0)
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

// epochBudgetKnown is true when every priced input is present. A nonzero
// count rate needs count. A nonzero static new-key rate needs serving_models.
func epochBudgetKnown(fp *types.FeeParams, countSource, modelsSource string) bool {
	if epochPrice(fp) == 0 {
		return true
	}
	_, rate, perNew := storeCommitRates(fp)
	if rate != 0 && countSource == countSourceNone {
		return false
	}
	if perNew != 0 && modelsSource == countSourceNone {
		return false
	}
	return true
}

func storeCommitRates(fp *types.FeeParams) (base, rate, gasPerNew uint64) {
	if fp == nil {
		return 0, 0, 0
	}
	group, rules := fp.RulesForTypeURL(sdk.MsgTypeURL(&types.MsgPoCV2StoreCommit{}))
	var baseRule *types.MsgGasRule
	for _, rule := range rules {
		if d := rule.GetStoredDelta(); d != nil {
			rate = d.GasPerUnit
			baseRule = rule
		}
		if s := rule.GetStatic(); s != nil {
			gasPerNew = s.Gas
		}
		if rule.Base != nil {
			baseRule = rule
		}
	}
	if pb := types.ResolvedPeriodBase(group, baseRule); pb != nil {
		base = pb.Gas
	}
	return base, rate, gasPerNew
}

// maxConfirmationPocs is how many sequential CPoCs can start in one epoch's
// trigger window. ExpectedConfirmationsPerEpoch is a mean, not a cap.
func maxConfirmationPocs(ep *types.EpochParams, cp *types.ConfirmationPoCParams) uint64 {
	if ep == nil || cp == nil || cp.ExpectedConfirmationsPerEpoch == 0 {
		return 0
	}
	// Last legal trigger: a CPoC started here must finish before the next
	// epoch PoC. Safety window belongs only in this bound, not in spacing.
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
	span := confirmationPocEventSpan(ep)
	n := (triggerWindowLength-1)/span + 1
	if n < 1 {
		return 1
	}
	return uint64(n)
}

// confirmationPocEventSpan is trigger → clear. The next CPoC may start on the
// clear block (BeginBlocker clears, then may trigger). Safety window is not
// part of this: it only keeps the last trigger from overlapping the next epoch.
func confirmationPocEventSpan(ep *types.EpochParams) int64 {
	if ep == nil {
		return 1
	}
	grace := ep.InferenceValidationCutoff
	if grace < 1 {
		grace = 1
	}
	span := grace +
		ep.PocStageDuration +
		ep.PocExchangeDuration +
		ep.PocValidationDelay +
		ep.PocValidationDuration +
		ep.SetNewValidatorsDelay - 1
	if span < 1 {
		return 1
	}
	return span
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
	if v > ^uint64(0)-v/2 {
		return ^uint64(0)
	}
	return v + v/2
}

func saturatingAdd(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}

func saturatingMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > ^uint64(0)/b {
		return ^uint64(0)
	}
	return a * b
}

func epochFeeBudgetNgonka(fp *types.FeeParams, ep *types.EpochParams, cp *types.ConfirmationPoCParams, count, servingModels uint64) math.Int {
	price := epochPrice(fp)
	if price <= 0 {
		return math.ZeroInt()
	}
	base, rate, gasPerNew := storeCommitRates(fp)
	stages := saturatingAdd(1, maxConfirmationPocs(ep, cp))
	nCommits := commitHeightsPerStage(ep)
	// DAPI default is (intrinsic + extra)×1.5 per StoreCommit. Hosts who set
	// 1.2 over-reserve slightly. Deltas sum to count; period base once per
	// stage; static gas once per serving model.
	perStageExtra := saturatingAdd(base, saturatingAdd(saturatingMul(rate, count), saturatingMul(gasPerNew, servingModels)))
	intrinsicPad := budgetHeadroom(budgetStoreCommitIntrinsic)
	commitGas := saturatingMul(stages, saturatingAdd(budgetHeadroom(perStageExtra), saturatingMul(nCommits, intrinsicPad)))
	hdGas := saturatingMul(hardwareDiffCount(ep), budgetHeadroom(typicalHardwareDiffGas))
	totalGas := saturatingAdd(commitGas, hdGas)
	return math.NewIntFromUint64(totalGas).MulRaw(price)
}
