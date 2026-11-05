package journal

import (
	"devshard/cmd/gateway/accounting"
	"devshard/types"
)

// DiffFactKind and DiffFact live in accounting, so the ledger takes them without importing journal.
type (
	DiffFactKind = accounting.DiffFactKind
	DiffFact     = accounting.DiffFact
)

const (
	DiffFactValidation     = accounting.DiffFactValidation
	DiffFactInvalidVerdict = accounting.DiffFactInvalidVerdict
	DiffFactAppliedTimeout = accounting.DiffFactAppliedTimeout
	DiffFactServiceNonce   = accounting.DiffFactServiceNonce
)

// diffFacts runs under the session lock, so a diff with no ledger fact allocates nothing. See README.md, "Order and the locks it takes".
func diffFacts(diff *types.Diff) []DiffFact {
	factCount := 0
	startsInference := false
	for _, tx := range diff.Txs {
		startsInference = startsInference || tx.GetStartInference() != nil
		if validation := tx.GetValidation(); validation != nil {
			factCount++
			if !validation.Valid {
				factCount++
			}
		} else if tx.GetTimeoutInference() != nil {
			factCount++
		}
	}
	if !startsInference {
		factCount++
	}
	if factCount == 0 {
		return nil
	}
	facts := make([]DiffFact, 0, factCount)
	for _, tx := range diff.Txs {
		switch validation, timeout := tx.GetValidation(), tx.GetTimeoutInference(); {
		case validation != nil:
			facts = append(facts, DiffFact{Kind: DiffFactValidation, Nonce: validation.InferenceId, ValidatorSlot: validation.ValidatorSlot})
			if !validation.Valid {
				facts = append(facts, DiffFact{Kind: DiffFactInvalidVerdict, Nonce: validation.InferenceId, ValidatorSlot: validation.ValidatorSlot})
			}
		case timeout != nil:
			facts = append(facts, DiffFact{Kind: DiffFactAppliedTimeout, Nonce: timeout.InferenceId})
		}
	}
	if !startsInference {
		facts = append(facts, DiffFact{Kind: DiffFactServiceNonce, Nonce: diff.Nonce, Purpose: servicePurpose(diff)})
	}
	return facts
}

// servicePurpose names what a diff with no inference was composed for. See docs/accounting.md, "Service nonces".
func servicePurpose(diff *types.Diff) accounting.ServicePurpose {
	var carriesFinalize, carriesHeartbeat, carriesErrorMiss, carriesTimeout, carriesHeightAck bool
	for _, tx := range diff.Txs {
		carriesFinalize = carriesFinalize || tx.GetFinalizeRound() != nil
		carriesHeartbeat = carriesHeartbeat || tx.GetHeartbeat() != nil || tx.GetForceHeightSyncTurn() != nil
		carriesErrorMiss = carriesErrorMiss || tx.GetErrorMiss() != nil
		carriesTimeout = carriesTimeout || tx.GetTimeoutInference() != nil
		carriesHeightAck = carriesHeightAck || tx.GetHeightAck() != nil
	}
	switch {
	case carriesFinalize:
		return accounting.ServiceFinalize
	case carriesHeartbeat:
		return accounting.ServiceHeartbeat
	case carriesErrorMiss:
		return accounting.ServiceErrorMiss
	case carriesTimeout:
		return accounting.ServiceTimeout
	case carriesHeightAck:
		return accounting.ServiceHeartbeat
	default:
		return accounting.ServiceFlush
	}
}
