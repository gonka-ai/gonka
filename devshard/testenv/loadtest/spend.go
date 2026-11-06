package loadtest

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"devshard/testenv/mockchain/ledgerface"
)

const (
	ngonkaPerGNK = 1e9
	spendSource  = "mock-chain /testenv/escrows and /testenv/txs?type=settle (spent = last settle's sum(host_stats.cost) + fees; nonces = its nonce); an unsettled escrow's nonces from the highest nonce the gateway reported in pending.jsonl or after drain; per completed request divides by the client's completed requests; inferences_completed from each escrow's /v1/debug/inferences after drain, else the last one read while the run was live"
)

// chainLedger is the mock-chain ledger read after drain by a run whose invariants did not read it.
type chainLedger struct {
	SettleTxs []ledgerface.TxView     `json:"settle_txs"`
	Escrows   []ledgerface.EscrowView `json:"escrows"`
	Error     string                  `json:"error,omitempty"`
}

// RunSummarySpend is what the escrows paid out by the end of the run, as the mock chain records it,
// so either gateway is measured alike. Unavailable maps a nil field to the reason it is missing.
type RunSummarySpend struct {
	Source      string        `json:"source"`
	Unavailable string        `json:"unavailable,omitempty"`
	Escrows     []EscrowSpend `json:"escrows"`
	Totals      SpendTotals   `json:"totals"`
}

// EscrowSpend is one escrow on the mock chain; spend and nonce come from its latest settle tx, and
// an unsettled escrow's nonces_used from the last nonce the gateway reported for it.
type EscrowSpend struct {
	EscrowID            string            `json:"escrow_id"`
	Model               string            `json:"model"`
	DepositNgonka       uint64            `json:"deposit_ngonka"`
	Settled             bool              `json:"settled"`
	SettleTxs           int               `json:"settle_txs"`
	SpentNgonka         *uint64           `json:"spent_ngonka"`
	HostCostNgonka      *uint64           `json:"host_cost_ngonka"`
	FeesNgonka          *uint64           `json:"fees_ngonka"`
	MaxNonce            *uint64           `json:"max_nonce"`
	NoncesUsed          *uint64           `json:"nonces_used"`
	InferencesCompleted *int              `json:"inferences_completed"`
	Unavailable         map[string]string `json:"unavailable,omitempty"`
}

// SpendTotals sums the settled escrows' spend and every escrow's nonces, and divides by the client's
// completed requests; Partial names the escrows a present total leaves out or estimates.
type SpendTotals struct {
	Escrows                   int               `json:"escrows"`
	SettledEscrows            int               `json:"settled_escrows"`
	CompletedRequests         int               `json:"completed_requests"`
	SpentNgonka               *uint64           `json:"spent_ngonka"`
	SpentGNK                  *float64          `json:"spent_gnk"`
	SettledNoncesUsed         *uint64           `json:"settled_nonces_used"`
	NoncesUsed                *uint64           `json:"nonces_used"`
	NgonkaPerCompletedRequest *float64          `json:"ngonka_per_completed_request"`
	NoncesPerCompletedRequest *float64          `json:"nonces_per_completed_request"`
	Partial                   map[string]string `json:"partial,omitempty"`
	Unavailable               map[string]string `json:"unavailable,omitempty"`
}

// spendSources returns the chain ledger and inference lists a run saved; the invariant read wins
// because it also carries the inference lists.
func spendSources(inputs RunInputs) (*chainLedger, map[string]map[string]DebugInference, string) {
	if end := inputs.Invariants; end != nil && end.ChainError != notSaved {
		inferences := end.Inferences
		if len(end.LastLiveInferences) > 0 {
			inferences = listsNotServed(end.LastLiveInferences, end.Inferences)
			maps.Insert(inferences, maps.All(end.Inferences))
		}
		return &chainLedger{SettleTxs: end.SettleTxs, Escrows: end.Escrows}, inferences, end.ChainError
	}
	if inputs.ChainLedger != nil {
		return inputs.ChainLedger, nil, inputs.ChainLedger.Error
	}
	return nil, nil, notSaved
}

// listsNotServed returns the live lists of the escrows the after-drain read did not serve; a
// gateway may prune a settled escrow's list before drain.
func listsNotServed(live, afterDrain map[string]map[string]DebugInference) map[string]map[string]DebugInference {
	lists := map[string]map[string]DebugInference{}
	for escrowID, inferences := range live {
		if _, served := afterDrain[escrowID]; !served {
			lists[escrowID] = inferences
		}
	}
	return lists
}

// readNonces returns the highest nonce the harness read for each escrow: from the pending poller
// while the run was live, and from the after-drain devshard list for an escrow still resident.
// It is nil when the run read neither.
func readNonces(pending []PendingSample, end *invariantEndState) map[string]uint64 {
	var devshards []GatewayDevshard
	if end != nil {
		devshards = end.Devshards
	}
	if pending == nil && devshards == nil {
		return nil
	}
	nonces := map[string]uint64{}
	for _, sample := range pending {
		if sample.EscrowID != "" && sample.Error == "" {
			nonces[sample.EscrowID] = max(nonces[sample.EscrowID], sample.Nonce)
		}
	}
	for _, devshard := range devshards {
		if devshard.Resident {
			nonces[devshard.ID] = max(nonces[devshard.ID], devshard.Nonce)
		}
	}
	return nonces
}

// buildSpend builds the spend block; nonces holds the nonces read from the gateway and completed is
// the client's completed request count.
func buildSpend(ledger *chainLedger, inferences map[string]map[string]DebugInference, nonces map[string]uint64, ledgerErr string, completed int) RunSummarySpend {
	spend := RunSummarySpend{Source: spendSource, Escrows: []EscrowSpend{}, Totals: SpendTotals{CompletedRequests: completed, Unavailable: map[string]string{}}}
	if ledger == nil || ledgerErr != "" {
		spend.Unavailable = "mock-chain ledger: " + cmp.Or(ledgerErr, notSaved)
		for _, field := range []string{"spent_ngonka", "spent_gnk", "settled_nonces_used", "nonces_used", "ngonka_per_completed_request", "nonces_per_completed_request"} {
			spend.Totals.Unavailable[field] = spend.Unavailable
		}
		return spend
	}
	latest := map[uint64]*ledgerface.SettleTxView{}
	counts := map[uint64]int{}
	for _, tx := range ledger.SettleTxs {
		if tx.Settle != nil {
			latest[tx.Settle.EscrowID] = tx.Settle
			counts[tx.Settle.EscrowID]++
		}
	}
	var spent, settledNonces, allNonces uint64
	var unsettled, unread []string
	for _, escrow := range ledger.Escrows {
		id := strconv.FormatUint(escrow.ID, 10)
		row := EscrowSpend{EscrowID: id, Model: escrow.ModelID, DepositNgonka: escrow.Amount, Settled: escrow.Settled, SettleTxs: counts[escrow.ID], Unavailable: map[string]string{}}
		if settle := latest[escrow.ID]; settle != nil {
			var cost uint64
			for _, stat := range settle.HostStats {
				cost += stat.Cost
			}
			total, fees, nonce := cost+settle.Fees, settle.Fees, settle.Nonce
			row.SpentNgonka, row.HostCostNgonka, row.FeesNgonka, row.MaxNonce, row.NoncesUsed = &total, &cost, &fees, &nonce, &nonce
			spent += total
			settledNonces += nonce
			spend.Totals.SettledEscrows++
		} else {
			unsettled = append(unsettled, id)
			reason := "not settled by the end of the run: the chain paid nothing out of it yet"
			for _, field := range []string{"spent_ngonka", "host_cost_ngonka", "fees_ngonka", "max_nonce"} {
				row.Unavailable[field] = reason
			}
			if nonce, ok := nonces[id]; ok {
				row.NoncesUsed = &nonce
			} else {
				unread = append(unread, id)
				if nonces == nil {
					row.Unavailable["nonces_used"] = "not settled, and the run read no gateway nonces (invariants off)"
				} else {
					row.Unavailable["nonces_used"] = "not settled, and the gateway reported no nonce for this escrow, while the run was live or after drain"
				}
			}
		}
		if row.NoncesUsed != nil {
			allNonces += *row.NoncesUsed
		}
		if records, ok := inferences[id]; ok {
			count := 0
			for _, inference := range records {
				// The statuses whose cost settlement carries, as escrow-accounting-reconciles counts them.
				switch inference.Status {
				case "finished", "validated", "challenged":
					count++
				}
			}
			row.InferencesCompleted = &count
		} else if inferences == nil {
			row.Unavailable["inferences_completed"] = "the chain carries no inference count and the run read no gateway inference lists (invariants off)"
		} else {
			row.Unavailable["inferences_completed"] = "the chain carries no inference count and the gateway served no inference list for this escrow, after drain or while the run was live"
		}
		if len(row.Unavailable) == 0 {
			row.Unavailable = nil
		}
		spend.Escrows = append(spend.Escrows, row)
	}
	slices.SortFunc(spend.Escrows, func(a, b EscrowSpend) int {
		x, _ := strconv.ParseUint(a.EscrowID, 10, 64)
		y, _ := strconv.ParseUint(b.EscrowID, 10, 64)
		return cmp.Compare(x, y)
	})
	totals := &spend.Totals
	totals.Escrows, totals.Partial = len(spend.Escrows), map[string]string{}
	slices.Sort(unsettled)
	slices.Sort(unread)
	unsettledReason := "escrows " + strings.Join(unsettled, ",") + " not settled by the end of the run: the chain paid nothing out of them yet"
	unreadReason := "escrows " + strings.Join(unread, ",") + " not settled and no gateway nonce read for them: their nonces are left out"
	noRequests := "no completed requests"
	if len(unread) < totals.Escrows {
		totals.NoncesUsed = &allNonces
		if len(unread) > 0 {
			totals.Partial["nonces_used"] = unreadReason
		}
		if completed > 0 {
			perRequest := float64(allNonces) / float64(completed)
			totals.NoncesPerCompletedRequest = &perRequest
			if len(unread) > 0 {
				totals.Partial["nonces_per_completed_request"] = unreadReason
			}
		} else {
			totals.Unavailable["nonces_per_completed_request"] = noRequests
		}
	} else {
		reason := fmt.Sprintf("no nonce read for any of the %d escrows", totals.Escrows)
		totals.Unavailable["nonces_used"] = reason
		totals.Unavailable["nonces_per_completed_request"] = reason
	}
	if totals.SettledEscrows == 0 {
		reason := fmt.Sprintf("none of the %d escrows was settled by the end of the run", totals.Escrows)
		for _, field := range []string{"spent_ngonka", "spent_gnk", "settled_nonces_used", "ngonka_per_completed_request"} {
			totals.Unavailable[field] = reason
		}
	} else {
		gnk := float64(spent) / ngonkaPerGNK
		totals.SpentNgonka, totals.SpentGNK, totals.SettledNoncesUsed = &spent, &gnk, &settledNonces
		if len(unsettled) > 0 {
			totals.Partial["spent_ngonka"] = unsettledReason
			totals.Partial["spent_gnk"] = unsettledReason
		}
		switch {
		case completed == 0:
			totals.Unavailable["ngonka_per_completed_request"] = noRequests
		case len(unsettled) == 0:
			perRequest := float64(spent) / float64(completed)
			totals.NgonkaPerCompletedRequest = &perRequest
		case settledNonces == 0:
			totals.Unavailable["ngonka_per_completed_request"] = unsettledReason + ", and the settled escrows used no nonce to price theirs by"
		default:
			// Only a settle prices nonces, so the unsettled escrows' nonces are priced at the settled escrows' rate.
			perNonce := float64(spent) / float64(settledNonces)
			perRequest := perNonce * float64(allNonces) / float64(completed)
			totals.NgonkaPerCompletedRequest = &perRequest
			reason := unsettledReason + fmt.Sprintf("; estimated: their nonces priced at the settled escrows' %.1f ngonka per nonce", perNonce)
			if len(unread) > 0 {
				reason += "; " + unreadReason
			}
			totals.Partial["ngonka_per_completed_request"] = reason
		}
	}
	if len(totals.Partial) == 0 {
		totals.Partial = nil
	}
	if len(totals.Unavailable) == 0 {
		totals.Unavailable = nil
	}
	return spend
}
