package tx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrTxNotFound marks a tx that is not on chain (gRPC NotFound) — distinct
// from one that committed but failed. The tx may still land until its
// unordered TTL elapses, so callers should treat this as "not yet" rather
// than "never".
var ErrTxNotFound = errors.New("chain tx: tx not found on chain")

// txHashFromBytes returns the cosmos tx hash (uppercase-hex SHA-256 of the tx
// bytes), computable before broadcast and equal to the hash the node returns.
func txHashFromBytes(txBytes []byte) string {
	sum := sha256.Sum256(txBytes)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// Manager signs and broadcasts chain transactions over gRPC.
type Manager struct {
	client grpc.ClientConnInterface
	cfg    Config

	keyring    KeyringSigner
	registry   codectypes.InterfaceRegistry
	hasKeyring bool
}

// New creates a Manager for unordered gateway txs.
func New(conn grpc.ClientConnInterface, cfg Config) (*Manager, error) {
	if conn == nil {
		return nil, fmt.Errorf("chain tx: gRPC connection is required")
	}
	return &Manager{
		client: conn,
		cfg:    cfg.withDefaults(),
	}, nil
}

// NewWithKeyring creates a Manager that can sign ordered txs via keyring.
func NewWithKeyring(conn grpc.ClientConnInterface, kr keyring.Keyring, address, signerKeyName, chainID string, cfg Config) (*Manager, error) {
	if strings.TrimSpace(chainID) == "" {
		chainID = DefaultChainID
	}
	if kr == nil {
		return nil, fmt.Errorf("keyring is required")
	}
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	inferencetypes.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	txConfig := authtx.NewTxConfig(cdc, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})
	m, err := New(conn, cfg)
	if err != nil {
		return nil, err
	}
	m.registry = registry
	m.hasKeyring = true
	m.keyring = KeyringSigner{
		Keyring:  kr,
		TxConfig: txConfig,
		KeyName:  signerKeyName,
		Address:  address,
	}
	m.cfg.ChainID = chainID
	return m, nil
}

// CreateDevshardEscrow broadcasts an unordered create escrow tx.
func (m *Manager) CreateDevshardEscrow(ctx context.Context, signer UnorderedSigner, amount uint64, modelID string) (*CreateDevshardEscrowResult, error) {
	return m.CreateDevshardEscrowWithIntent(ctx, signer, amount, modelID, nil)
}

// CreateDevshardEscrowWithIntent is like CreateDevshardEscrow, but invokes
// onPrepared with the precomputed tx hash before the irreversible broadcast.
// This lets callers persist a write-ahead commitment (keyed by tx hash) so an
// escrow create can be recovered even if the process dies between broadcast
// and the local record of its result.
func (m *Manager) CreateDevshardEscrowWithIntent(ctx context.Context, signer UnorderedSigner, amount uint64, modelID string, onPrepared func(txHash string) error) (*CreateDevshardEscrowResult, error) {
	if m == nil {
		return nil, fmt.Errorf("chain tx manager is nil")
	}
	if signer == nil {
		return nil, fmt.Errorf("signer is required")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil, fmt.Errorf("model_id is required")
	}
	if amount == 0 {
		return nil, fmt.Errorf("amount is required")
	}
	chainID, err := m.chainID(ctx)
	if err != nil {
		return nil, err
	}
	account, err := m.accountInfo(ctx, signer.Address())
	if err != nil {
		return nil, err
	}
	txBytes, err := buildCreateDevshardEscrowTx(signer, chainID, account, m.cfg.FeeDenom, m.cfg.FeeAmount, m.cfg.GasLimit, amount, modelID)
	if err != nil {
		return nil, err
	}
	// Record the intent (precomputed hash) before the irreversible broadcast; abort if it fails.
	txHash := txHashFromBytes(txBytes)
	if onPrepared != nil {
		if err := onPrepared(txHash); err != nil {
			return nil, fmt.Errorf("record escrow create intent before broadcast: %w", err)
		}
	}
	broadcastHash, err := m.broadcastTx(ctx, txBytes)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(broadcastHash, txHash) {
		return nil, fmt.Errorf("tx hash mismatch: precomputed %s, node returned %s", txHash, broadcastHash)
	}
	escrowID, err := m.waitForCreatedEscrowID(ctx, txHash)
	if err != nil {
		return nil, err
	}
	return &CreateDevshardEscrowResult{
		EscrowID: escrowID,
		TxHash:   txHash,
		Creator:  signer.Address(),
	}, nil
}

// GetTxEscrowID resolves a create tx hash to its escrow_id in one lookup;
// found=false when the tx committed but failed (no escrow), and
// ErrTxNotFound when the tx is absent from the chain (never landed, or not
// yet indexed).
func (m *Manager) GetTxEscrowID(ctx context.Context, txHash string) (uint64, bool, error) {
	if m == nil {
		return 0, false, fmt.Errorf("chain tx manager is nil")
	}
	resp, err := m.getTx(ctx, txHash)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return 0, false, ErrTxNotFound
		}
		return 0, false, err
	}
	if resp.Code != 0 {
		return 0, false, nil
	}
	escrowID, ok := CreatedEscrowIDFromTxResponse(resp)
	if !ok {
		return 0, false, fmt.Errorf("tx %s committed but escrow_id event was not found", txHash)
	}
	return escrowID, true, nil
}

// SettleDevshardEscrow broadcasts an unordered settle escrow tx and waits until
// GetTx shows the tx committed with Code == 0 (DeliverTx success). SYNC CheckTx
// alone is not treated as success — callers must not clear settlement_pending
// until this returns.
func (m *Manager) SettleDevshardEscrow(ctx context.Context, signer UnorderedSigner, settlement SettleParams) (*SettleDevshardEscrowResult, error) {
	if m == nil {
		return nil, fmt.Errorf("chain tx manager is nil")
	}
	if signer == nil {
		return nil, fmt.Errorf("signer is required")
	}
	if settlement.EscrowID == 0 {
		return nil, fmt.Errorf("escrow_id is required")
	}
	chainID, err := m.chainID(ctx)
	if err != nil {
		return nil, err
	}
	account, err := m.accountInfo(ctx, signer.Address())
	if err != nil {
		return nil, err
	}
	txBytes, err := buildSettleDevshardEscrowTx(signer, chainID, account, m.cfg.FeeDenom, m.cfg.FeeAmount, m.cfg.GasLimit, settlement)
	if err != nil {
		return nil, err
	}
	txHash, err := m.broadcastTx(ctx, txBytes)
	if err != nil {
		return nil, err
	}
	if err := m.waitForTxSuccess(ctx, txHash); err != nil {
		return nil, err
	}
	return &SettleDevshardEscrowResult{
		EscrowID: settlement.EscrowID,
		TxHash:   txHash,
		Settler:  signer.Address(),
	}, nil
}

// SubmitDisputeState signs and broadcasts MsgSettleDevshardEscrow with dispute fields.
func (m *Manager) SubmitDisputeState(ctx context.Context, escrowID uint64, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error {
	if !m.hasKeyring {
		return fmt.Errorf("chain tx: keyring signer is required for ordered txs")
	}
	signatures := make([]*inferencetypes.DevshardSlotSignature, 0, len(sigs))
	for slotID, sig := range sigs {
		signatures = append(signatures, &inferencetypes.DevshardSlotSignature{
			SlotId:    slotID,
			Signature: sig,
		})
	}
	acc, err := m.accountInfo(ctx, m.keyring.Address)
	if err != nil {
		return fmt.Errorf("chain tx: get account info: %w", err)
	}
	accNum, seq := acc.AccountNumber, acc.Sequence
	msg := &inferencetypes.MsgSettleDevshardEscrow{
		Settler:    m.keyring.Address,
		EscrowId:   escrowID,
		StateRoot:  stateRoot,
		Nonce:      nonce,
		Signatures: signatures,
	}
	factory := clienttx.Factory{}.
		WithKeybase(m.keyring.Keyring).
		WithTxConfig(m.keyring.TxConfig).
		WithChainID(m.cfg.ChainID).
		WithAccountNumber(accNum).
		WithSequence(seq).
		WithGas(200_000).
		WithGasPrices("0ngonka").
		WithFromName(m.keyring.KeyName)
	txBuilder, err := factory.BuildUnsignedTx(msg)
	if err != nil {
		return fmt.Errorf("chain tx: build MsgSettleDevshardEscrow (dispute): %w", err)
	}
	if err := clienttx.Sign(ctx, factory, m.keyring.KeyName, txBuilder, true); err != nil {
		return fmt.Errorf("chain tx: sign MsgSettleDevshardEscrow (dispute): %w", err)
	}
	raw, err := m.keyring.TxConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return fmt.Errorf("chain tx: encode MsgSettleDevshardEscrow (dispute): %w", err)
	}
	txHash, err := m.broadcastTx(ctx, raw)
	if err != nil {
		return err
	}
	return m.waitForTxSuccess(ctx, txHash)
}

func (m *Manager) chainID(ctx context.Context) (string, error) {
	_ = ctx
	if id := strings.TrimSpace(m.cfg.ChainID); id != "" {
		return id, nil
	}
	// withDefaults should have filled this; keep a hard fallback for safety.
	return DefaultChainID, nil
}

func (m *Manager) accountInfo(ctx context.Context, address string) (chainAccount, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return chainAccount{}, fmt.Errorf("address is required")
	}
	qc := authtypes.NewQueryClient(m.client)
	res, err := qc.Account(ctx, &authtypes.QueryAccountRequest{Address: address})
	if err != nil {
		return chainAccount{}, fmt.Errorf("fetch account %s: %w", address, err)
	}
	if m.registry == nil {
		m.registry = codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(m.registry)
		authtypes.RegisterInterfaces(m.registry)
	}
	var acc sdk.AccountI
	if err := m.registry.UnpackAny(res.Account, &acc); err != nil {
		return chainAccount{}, fmt.Errorf("unpack account: %w", err)
	}
	return chainAccount{
		AccountNumber: acc.GetAccountNumber(),
		Sequence:      acc.GetSequence(),
	}, nil
}

func (m *Manager) broadcastTx(ctx context.Context, txBytes []byte) (string, error) {
	svc := txtypes.NewServiceClient(m.client)
	resp, err := svc.BroadcastTx(ctx, &txtypes.BroadcastTxRequest{
		TxBytes: txBytes,
		Mode:    txtypes.BroadcastMode_BROADCAST_MODE_SYNC,
	})
	if err != nil {
		return "", fmt.Errorf("broadcast tx: %w", err)
	}
	if resp.TxResponse.Code != 0 {
		return "", fmt.Errorf("broadcast tx failed code=%d codespace=%s raw_log=%s",
			resp.TxResponse.Code, resp.TxResponse.Codespace, resp.TxResponse.RawLog)
	}
	txHash := strings.TrimSpace(resp.TxResponse.TxHash)
	if txHash == "" {
		return "", fmt.Errorf("broadcast response missing txhash")
	}
	return txHash, nil
}

func (m *Manager) waitForCreatedEscrowID(ctx context.Context, txHash string) (uint64, error) {
	deadline := time.Now().Add(m.cfg.PollTimeout)
	var lastErr error
	for {
		resp, err := m.getTx(ctx, txHash)
		if err == nil {
			if resp.Code != 0 {
				return 0, fmt.Errorf("tx %s failed code=%d codespace=%s raw_log=%s",
					txHash, resp.Code, resp.Codespace, resp.RawLog)
			}
			if escrowID, ok := CreatedEscrowIDFromTxResponse(resp); ok {
				return escrowID, nil
			}
			lastErr = fmt.Errorf("tx %s committed but escrow_id event was not found", txHash)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("wait for tx %s: %w", txHash, lastErr)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(m.cfg.PollInterval):
		}
	}
}

// waitForTxSuccess polls GetTx until the tx is indexed with Code == 0 (DeliverTx
// success). NotFound / index lag retries until PollTimeout.
func (m *Manager) waitForTxSuccess(ctx context.Context, txHash string) error {
	deadline := time.Now().Add(m.cfg.PollTimeout)
	var lastErr error
	for {
		resp, err := m.getTx(ctx, txHash)
		if err == nil {
			if resp.Code != 0 {
				return fmt.Errorf("tx %s failed code=%d codespace=%s raw_log=%s",
					txHash, resp.Code, resp.Codespace, resp.RawLog)
			}
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("wait for tx %s: %w", txHash, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.cfg.PollInterval):
		}
	}
}

func (m *Manager) getTx(ctx context.Context, txHash string) (*sdk.TxResponse, error) {
	svc := txtypes.NewServiceClient(m.client)
	resp, err := svc.GetTx(ctx, &txtypes.GetTxRequest{Hash: txHash})
	if err != nil {
		return nil, err
	}
	return resp.TxResponse, nil
}
