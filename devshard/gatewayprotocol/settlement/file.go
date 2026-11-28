package settlement

import (
	"encoding/base64"
	"strconv"
)

// File is the settlement file `inferenced tx inference settle-devshard-escrow` reads: the escrow id as a decimal string, bytes as standard base64, and `version` for the state-root-and-protocol-version string.
type File struct {
	EscrowID   string          `json:"escrow_id"`
	Version    string          `json:"version"`
	StateRoot  string          `json:"state_root"`
	Nonce      uint64          `json:"nonce"`
	Fees       uint64          `json:"fees"`
	RestHash   string          `json:"rest_hash"`
	HostStats  []FileHostStat  `json:"host_stats"`
	Signatures []FileSignature `json:"signatures"`
}

// FileHostStat is one host-stats entry of a File; the four counters after `cost` are omitted when zero.
type FileHostStat struct {
	SlotID               uint32 `json:"slot_id"`
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations,omitempty"`
	CompletedValidations uint32 `json:"completed_validations,omitempty"`
	Validated            uint32 `json:"validated,omitempty"`
	Finished             uint32 `json:"finished,omitempty"`
}

// FileSignature is one slot signature of a File.
type FileSignature struct {
	SlotID    uint32 `json:"slot_id"`
	Signature string `json:"signature"`
}

// NewFile maps message onto the settlement file, keeping the order of its host stats and signatures.
func NewFile(message Message) File {
	hostStats := make([]FileHostStat, 0, len(message.HostStats))
	for _, hostStat := range message.HostStats {
		hostStats = append(hostStats, FileHostStat(hostStat))
	}
	signatures := make([]FileSignature, 0, len(message.SlotSignatures))
	for _, slotSignature := range message.SlotSignatures {
		signatures = append(signatures, FileSignature{SlotID: slotSignature.SlotID, Signature: base64.StdEncoding.EncodeToString(slotSignature.Signature)})
	}
	return File{
		EscrowID:   strconv.FormatUint(message.EscrowID, 10),
		Version:    message.Version,
		StateRoot:  base64.StdEncoding.EncodeToString(message.StateRoot),
		Nonce:      message.Nonce,
		Fees:       message.Fees,
		RestHash:   base64.StdEncoding.EncodeToString(message.RestHash),
		HostStats:  hostStats,
		Signatures: signatures,
	}
}
