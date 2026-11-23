// Package settlementwire maps a settlement message onto the chain's MsgSettleDevshardEscrow and marshals it. See README.md.
package settlementwire

import (
	inferencetypes "github.com/productscience/inference/x/inference/types"

	"devshard/gatewayprotocol/settlement"
)

// Marshal encodes message as the MsgSettleDevshardEscrow that settler signs, through the chain's generated type.
func Marshal(settler string, message settlement.Message) ([]byte, error) {
	chainMessage := &inferencetypes.MsgSettleDevshardEscrow{
		Settler:                     settler,
		EscrowId:                    message.EscrowID,
		StateRoot:                   message.StateRoot,
		Nonce:                       message.Nonce,
		RestHash:                    message.RestHash,
		HostStats:                   chainHostStats(message.HostStats),
		Signatures:                  chainSignatures(message.SlotSignatures),
		Fees:                        message.Fees,
		StateRootAndProtocolVersion: message.Version,
	}
	return chainMessage.Marshal()
}

func chainHostStats(stats []settlement.HostStat) []*inferencetypes.DevshardSettlementHostStats {
	chainStats := make([]*inferencetypes.DevshardSettlementHostStats, 0, len(stats))
	for _, hostStat := range stats {
		chainStats = append(chainStats, &inferencetypes.DevshardSettlementHostStats{
			SlotId:               hostStat.SlotID,
			Missed:               hostStat.Missed,
			Invalid:              hostStat.Invalid,
			Cost:                 hostStat.Cost,
			RequiredValidations:  hostStat.RequiredValidations,
			CompletedValidations: hostStat.CompletedValidations,
		})
	}
	return chainStats
}

func chainSignatures(signatures []settlement.SlotSignature) []*inferencetypes.DevshardSlotSignature {
	chainSlotSignatures := make([]*inferencetypes.DevshardSlotSignature, 0, len(signatures))
	for _, slotSignature := range signatures {
		chainSlotSignatures = append(chainSlotSignatures, &inferencetypes.DevshardSlotSignature{
			SlotId:    slotSignature.SlotID,
			Signature: slotSignature.Signature,
		})
	}
	return chainSlotSignatures
}
