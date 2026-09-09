package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"

	chaintx "common/chain/tx"

	"google.golang.org/grpc"
)

// CreateDevshardEscrowResult is returned after a successful on-chain create tx.
type CreateDevshardEscrowResult struct {
	EscrowID uint64 `json:"escrow_id"`
	TxHash   string `json:"tx_hash"`
	Creator  string `json:"creator"`
}

// SettleDevshardEscrowResult is returned after a successful on-chain settle tx.
type SettleDevshardEscrowResult struct {
	EscrowID uint64 `json:"escrow_id"`
	TxHash   string `json:"tx_hash"`
	Settler  string `json:"settler"`
}

func createEscrowResultFromTx(r *chaintx.CreateDevshardEscrowResult) *CreateDevshardEscrowResult {
	if r == nil {
		return nil
	}
	return &CreateDevshardEscrowResult{
		EscrowID: r.EscrowID,
		TxHash:   r.TxHash,
		Creator:  r.Creator,
	}
}

func settleEscrowResultFromTx(r *chaintx.SettleDevshardEscrowResult) *SettleDevshardEscrowResult {
	if r == nil {
		return nil
	}
	return &SettleDevshardEscrowResult{
		EscrowID: r.EscrowID,
		TxHash:   r.TxHash,
		Settler:  r.Settler,
	}
}

// errTxNotFound mirrors chaintx.ErrTxNotFound for callers that only import
// reconciliation logic ported from upgrade-0.2.14 needs no changes).
var errTxNotFound = chaintx.ErrTxNotFound

func newGatewayChainTxClient(conn grpc.ClientConnInterface, settings GatewaySettings, chainID, feeDenom string, feeAmount, gasLimit uint64) (*chaintx.Manager, error) {
	if conn == nil {
		return nil, fmt.Errorf("chain gRPC connection is required")
	}
	return chaintx.New(conn, chaintx.Config{
		ChainID:      firstNonEmpty(chainID, os.Getenv("DEVSHARD_CHAIN_ID"), chaintx.DefaultChainID),
		FeeDenom:     firstNonEmpty(feeDenom, os.Getenv("DEVSHARD_TX_FEE_DENOM"), chaintx.DefaultFeeDenom),
		FeeAmount:    firstNonZeroUint64(feeAmount, uint64(readInt64Env("DEVSHARD_TX_FEE_AMOUNT", int64(chaintx.DefaultFeeAmount)))),
		GasLimit:     firstNonZeroUint64(gasLimit, settings.TxGasLimit, uint64(readInt64Env("DEVSHARD_TX_GAS_LIMIT", int64(chaintx.DefaultGasLimit)))),
		PollInterval: txSettingDurationMS(os.Getenv("DEVSHARD_TX_POLL_INTERVAL_MS"), chaintx.DefaultPollInterval),
		PollTimeout:  txSettingDurationMS(os.Getenv("DEVSHARD_TX_POLL_TIMEOUT_MS"), chaintx.DefaultPollTimeout),
	})
}

func settleParamsFromJSON(settlement SettlementJSON) (chaintx.SettleParams, error) {
	stateRoot, err := decodeBase64Field(settlement.StateRoot, "state_root")
	if err != nil {
		return chaintx.SettleParams{}, err
	}
	restHash, err := decodeBase64Field(settlement.RestHash, "rest_hash")
	if err != nil {
		return chaintx.SettleParams{}, err
	}
	escrowID, err := parseEscrowIDString(settlement.EscrowID)
	if err != nil {
		return chaintx.SettleParams{}, err
	}
	hostStats := make([]chaintx.HostStats, 0, len(settlement.HostStats))
	for _, hs := range settlement.HostStats {
		hostStats = append(hostStats, chaintx.HostStats{
			SlotID:               uint32(hs.SlotID),
			Missed:               int32(hs.Missed),
			Invalid:              int32(hs.Invalid),
			Cost:                 hs.Cost,
			RequiredValidations:  int32(hs.RequiredValidations),
			CompletedValidations: int32(hs.CompletedValidations),
		})
	}
	sigs := make([]chaintx.SlotSignature, 0, len(settlement.Signatures))
	for _, sig := range settlement.Signatures {
		sigBytes, err := decodeBase64Field(sig.Signature, fmt.Sprintf("signature slot %d", sig.SlotID))
		if err != nil {
			return chaintx.SettleParams{}, err
		}
		sigs = append(sigs, chaintx.SlotSignature{SlotID: uint32(sig.SlotID), Signature: sigBytes})
	}
	return chaintx.SettleParams{
		EscrowID:                    escrowID,
		StateRoot:                   stateRoot,
		Nonce:                       settlement.Nonce,
		RestHash:                    restHash,
		HostStats:                   hostStats,
		Signatures:                  sigs,
		Fees:                        settlement.Fees,
		StateRootAndProtocolVersion: []byte(settlement.StateRootAndProtocolVersion),
	}, nil
}

func parseEscrowIDString(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("escrow_id is required")
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid escrow_id %q", raw)
	}
	return id, nil
}

func decodeBase64Field(raw, field string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	out, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", field, err)
	}
	return out, nil
}
