package bls

import (
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/utils"
	"decentralized-api/logging"
	"fmt"
	"strconv"

	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

func (bm *BlsManager) ProcessDisputePhaseStarted(event *chainevents.JSONRPCResponse) error {
	epochIDStr, err := bm.extractEventString(event, "inference.bls.EventDisputePhaseStarted.epoch_id")
	if err != nil {
		return fmt.Errorf("failed to extract dispute phase epoch id: %w", err)
	}
	epochID, err := strconv.ParseUint(epochIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse dispute phase epoch id %q: %w", epochIDStr, err)
	}

	epochData, err := bm.extractEpochDataFromDisputeEvent(event)
	if err != nil {
		return fmt.Errorf("failed to extract dispute phase epoch data: %w", err)
	}

	myAddress := bm.cosmosClient.GetAccountAddress()
	dealerIndex := -1
	for i := range epochData.Participants {
		if epochData.Participants[i].Address == myAddress {
			dealerIndex = i
			break
		}
	}
	if dealerIndex < 0 {
		return nil
	}

	responses := make([]types.DealerComplaintResponse, 0, len(epochData.DealerComplaints))
	for i := range epochData.DealerComplaints {
		complaint := epochData.DealerComplaints[i]
		if int(complaint.DealerIndex) != dealerIndex {
			continue
		}
		disputedSlot := complaint.DisputedSlotIndex
		ciphertextIndex := complaint.DisputedCiphertextIndex

		record, ok := bm.getDealerOpeningRecord(epochID, complaint.ComplainerIndex, ciphertextIndex)
		if !ok {
			logging.Error(blsLogTag+"Missing opening record for complaint response", inferenceTypes.BLS,
				"epochID", epochID, "dealerIndex", dealerIndex, "complainerIndex", complaint.ComplainerIndex, "ciphertextIndex", ciphertextIndex)
			continue
		}
		if record.slotIndex != disputedSlot {
			logging.Error(blsLogTag+"Opening record slot mismatch for complaint response", inferenceTypes.BLS,
				"epochID", epochID, "dealerIndex", dealerIndex, "complainerIndex", complaint.ComplainerIndex, "ciphertextIndex", ciphertextIndex,
				"expectedSlot", disputedSlot, "actualSlot", record.slotIndex)
			continue
		}
		if len(record.shareBytes) != 32 || len(record.seed) != dkgOpeningSeedLen {
			logging.Error(blsLogTag+"Opening record payload malformed for complaint response", inferenceTypes.BLS,
				"epochID", epochID, "dealerIndex", dealerIndex, "complainerIndex", complaint.ComplainerIndex, "ciphertextIndex", ciphertextIndex,
				"shareLen", len(record.shareBytes), "seedLen", len(record.seed))
			continue
		}

		responses = append(responses, types.DealerComplaintResponse{
			ComplainerIndex:         complaint.ComplainerIndex,
			ResponseShareBytes:      record.shareBytes,
			ResponseOpeningMaterial: record.seed,
		})
	}

	if len(responses) == 0 {
		return nil
	}

	msg := &types.MsgRespondDealerComplaints{
		EpochId:     epochID,
		DealerIndex: uint32(dealerIndex),
		Responses:   responses,
	}
	if err := bm.cosmosClient.RespondDealerComplaints(msg); err != nil {
		logging.Warn(blsLogTag+"Failed to submit dealer complaint responses on dispute start", inferenceTypes.BLS,
			"epochID", epochID, "dealerIndex", dealerIndex, "responses", len(responses), "error", err)
		return nil
	}

	logging.Info(blsLogTag+"Submitted dealer complaint responses on dispute start", inferenceTypes.BLS,
		"epochID", epochID, "dealerIndex", dealerIndex, "submittedResponses", len(responses))

	return nil
}

func (bm *BlsManager) extractEpochDataFromDisputeEvent(event *chainevents.JSONRPCResponse) (*types.EpochBLSData, error) {
	epochDataStrs, ok := event.Result.Events["inference.bls.EventDisputePhaseStarted.epoch_data"]
	if !ok || len(epochDataStrs) == 0 {
		return nil, fmt.Errorf("epoch_data not found in dispute phase started event")
	}
	unquotedEpochData, err := utils.UnquoteEventValue(epochDataStrs[0])
	if err != nil {
		return nil, fmt.Errorf("failed to unquote epoch_data: %w", err)
	}
	epochData, err := bm.parseEpochDataFromJSON(unquotedEpochData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse epoch_data: %w", err)
	}
	return epochData, nil
}
