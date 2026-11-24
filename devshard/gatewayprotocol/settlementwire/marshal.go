// Package settlementwire maps a settlement message onto the chain's MsgSettleDevshardEscrow and marshals it. See README.md.
package settlementwire

import (
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/protobuf/encoding/protowire"

	"devshard/gatewayprotocol/settlement"
)

// Marshal encodes message as the MsgSettleDevshardEscrow that settler signs. Each host-stats entry carries Validated and Finished as fields 7 and 8, omitted when zero.
func Marshal(settler string, message settlement.Message) ([]byte, error) {
	encoded, err := (&inferencetypes.MsgSettleDevshardEscrow{
		Settler:   settler,
		EscrowId:  message.EscrowID,
		StateRoot: message.StateRoot,
		Nonce:     message.Nonce,
		RestHash:  message.RestHash,
	}).Marshal()
	if err != nil {
		return nil, err
	}
	for _, hostStat := range message.HostStats {
		entry, err := hostStatsEntry(hostStat)
		if err != nil {
			return nil, err
		}
		encoded = protowire.AppendTag(encoded, hostStatsFieldNumber, protowire.BytesType)
		encoded = protowire.AppendBytes(encoded, entry)
	}
	trailer, err := (&inferencetypes.MsgSettleDevshardEscrow{
		Signatures:                  chainSignatures(message.SlotSignatures),
		Fees:                        message.Fees,
		StateRootAndProtocolVersion: message.Version,
	}).Marshal()
	if err != nil {
		return nil, err
	}
	return append(encoded, trailer...), nil
}

func hostStatsEntry(hostStat settlement.HostStat) ([]byte, error) {
	entry, err := (&inferencetypes.DevshardSettlementHostStats{
		SlotId:               hostStat.SlotID,
		Missed:               hostStat.Missed,
		Invalid:              hostStat.Invalid,
		Cost:                 hostStat.Cost,
		RequiredValidations:  hostStat.RequiredValidations,
		CompletedValidations: hostStat.CompletedValidations,
	}).Marshal()
	if err != nil {
		return nil, err
	}
	entry = appendNonZeroCounter(entry, validatedFieldNumber, hostStat.Validated)
	return appendNonZeroCounter(entry, finishedFieldNumber, hostStat.Finished), nil
}

func appendNonZeroCounter(encoded []byte, number protowire.Number, value uint32) []byte {
	if value == 0 {
		return encoded
	}
	encoded = protowire.AppendTag(encoded, number, protowire.VarintType)
	return protowire.AppendVarint(encoded, uint64(value))
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
