package gatewayproto

import (
	"encoding/binary"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// MarshalLegacySettlement preserves the manual wire profile, including zero and
// empty fields. Its input order is retained; adapters own their list ordering.
func MarshalLegacySettlement(settler string, input SettlementInput) ([]byte, error) {
	var out []byte
	out = legacyBytesField(out, 1, []byte(settler))
	out = legacyVarintField(out, 2, input.EscrowID)
	out = legacyBytesField(out, 3, input.StateRoot)
	out = legacyVarintField(out, 4, input.Nonce)
	out = legacyBytesField(out, 5, input.RestHash)
	for _, hs := range input.HostStats {
		out = legacyBytesField(out, 6, MarshalLegacyHostStat(hs))
	}
	for _, sig := range input.SlotSigs {
		out = legacyBytesField(out, 7, MarshalLegacySlotSignature(sig))
	}
	out = legacyVarintField(out, 8, input.Fees)
	out = legacyBytesField(out, 9, []byte(input.Version))
	return out, nil
}

func MarshalLegacyHostStat(hs SettlementHostStat) []byte {
	var out []byte
	out = legacyVarintField(out, 1, uint64(hs.SlotID))
	out = legacyVarintField(out, 2, uint64(hs.Missed))
	out = legacyVarintField(out, 3, uint64(hs.Invalid))
	out = legacyVarintField(out, 4, hs.Cost)
	out = legacyVarintField(out, 5, uint64(hs.RequiredValidations))
	out = legacyVarintField(out, 6, uint64(hs.CompletedValidations))
	return out
}

func MarshalLegacySlotSignature(sig SettlementSlotSig) []byte {
	var out []byte
	out = legacyVarintField(out, 1, sig.SlotID)
	return legacyBytesField(out, 2, sig.Signature)
}

func legacyVarintField(dst []byte, fieldNumber int, value uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(fieldNumber<<3))
	return binary.AppendUvarint(dst, value)
}

func legacyBytesField(dst []byte, fieldNumber int, value []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(fieldNumber<<3|2))
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

// MarshalProtoSettlement preserves the generated protobuf profile, including
// its existing slot signature conversion.
func MarshalProtoSettlement(settler string, input SettlementInput) ([]byte, error) {
	hostStats := make([]*inferencetypes.DevshardSettlementHostStats, 0, len(input.HostStats))
	for _, hostStat := range input.HostStats {
		hostStats = append(hostStats, &inferencetypes.DevshardSettlementHostStats{
			SlotId:               hostStat.SlotID,
			Missed:               hostStat.Missed,
			Invalid:              hostStat.Invalid,
			Cost:                 hostStat.Cost,
			RequiredValidations:  hostStat.RequiredValidations,
			CompletedValidations: hostStat.CompletedValidations,
		})
	}
	signatures := make([]*inferencetypes.DevshardSlotSignature, 0, len(input.SlotSigs))
	for _, slotSig := range input.SlotSigs {
		signatures = append(signatures, &inferencetypes.DevshardSlotSignature{
			SlotId:    uint32(slotSig.SlotID),
			Signature: slotSig.Signature,
		})
	}
	message := &inferencetypes.MsgSettleDevshardEscrow{
		Settler:                     settler,
		EscrowId:                    input.EscrowID,
		StateRoot:                   input.StateRoot,
		Nonce:                       input.Nonce,
		RestHash:                    input.RestHash,
		HostStats:                   hostStats,
		Signatures:                  signatures,
		Fees:                        input.Fees,
		StateRootAndProtocolVersion: input.Version,
	}
	return message.Marshal()
}
