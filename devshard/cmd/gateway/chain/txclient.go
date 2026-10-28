// Package chain builds/signs/broadcasts gonka escrow transactions and
// observes chain phase state.
package chain

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"devshard/signing"
)

// See README.md, "Transaction encoding".
const (
	DefaultFeeDenom     = EscrowDenom
	DefaultFeeAmount    = uint64(1_000_000)
	DefaultGasLimit     = uint64(700_000)
	DefaultPollInterval = 2 * time.Second
	DefaultPollTimeout  = 45 * time.Second
	UnorderedTxTTL      = 9 * time.Minute

	EscrowDenom = "ngonka"
)

// ErrWalletUnderfunded marks a create refused before it was prepared, because the wallet cannot pay for it.
var ErrWalletUnderfunded = errors.New("wallet cannot pay for the escrow")

// WalletUnderfundedError names the wallet and the shortfall of a refused create.
type WalletUnderfundedError struct {
	Address string
	Have    uint64
	Need    uint64
}

func (e *WalletUnderfundedError) Error() string {
	return fmt.Sprintf("%s: %s holds %d%s, needs %d", ErrWalletUnderfunded, e.Address, e.Have, EscrowDenom, e.Need)
}

func (e *WalletUnderfundedError) Unwrap() error { return ErrWalletUnderfunded }

// ErrTxNotFound marks a tx absent from the chain, which is not terminal; ErrTxRejected marks one the node refused at CheckTx, which can never land. See README.md, "Transaction encoding".
var (
	ErrTxNotFound = errors.New("tx not found on chain")
	ErrTxRejected = errors.New("tx rejected at CheckTx")
)

// TxClient owns the transaction envelope and the intent hook; the transport underneath is interchangeable, which keeps its tests off a connection.
type TxClient struct {
	transport    Transport
	feeDenom     string
	feeAmount    uint64
	gasLimit     uint64
	pollInterval time.Duration
	pollTimeout  time.Duration
	now          func() time.Time
	narrator     settlementNarrator
}

// Config configures a TxClient. Zero-value fee/gas/poll/clock fields take package defaults; Transport is required.
type Config struct {
	Transport    Transport
	FeeDenom     string
	FeeAmount    uint64
	GasLimit     uint64
	PollInterval time.Duration
	PollTimeout  time.Duration
	Now          func() time.Time
	Narrator     settlementNarrator
}

// settlementNarrator is satisfied by *journal.Journal; chain never imports it. See README.md, "Boundaries".
type settlementNarrator interface {
	SettleBroadcast(escrowID, txHash, settler string)
}

// The tags are what the operator API returns; without them these are the only responses rendered with Go field names.
type CreateEscrowResult struct {
	EscrowID uint64 `json:"escrow_id"`
	TxHash   string `json:"tx_hash"`
	Creator  string `json:"creator"`
}

type SettleEscrowResult struct {
	EscrowID uint64 `json:"escrow_id"`
	TxHash   string `json:"tx_hash"`
	Settler  string `json:"settler"`
}

// NewTxClient validates cfg and applies defaults for unset fields; it errors only when no transport is given.
func NewTxClient(cfg Config) (*TxClient, error) {
	if cfg.Transport == nil {
		return nil, fmt.Errorf("chain transport is required")
	}
	feeDenom := strings.TrimSpace(cfg.FeeDenom)
	if feeDenom == "" {
		feeDenom = DefaultFeeDenom
	}
	feeAmount := cfg.FeeAmount
	if feeAmount == 0 {
		feeAmount = DefaultFeeAmount
	}
	gasLimit := cfg.GasLimit
	if gasLimit == 0 {
		gasLimit = DefaultGasLimit
	}
	pollInterval := cfg.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	pollTimeout := cfg.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = DefaultPollTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &TxClient{
		transport:    cfg.Transport,
		feeDenom:     feeDenom,
		feeAmount:    feeAmount,
		gasLimit:     gasLimit,
		pollInterval: pollInterval,
		pollTimeout:  pollTimeout,
		now:          now,
		narrator:     cfg.Narrator,
	}, nil
}

// CreateEscrow builds, signs and broadcasts a MsgCreateDevshardEscrow tx; onPrepared records the hash before the irreversible broadcast. See README.md, "Transaction encoding".
func (c *TxClient) CreateEscrow(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(txHash string) error) (CreateEscrowResult, error) {
	if signer == nil {
		return CreateEscrowResult{}, fmt.Errorf("signer is required")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return CreateEscrowResult{}, fmt.Errorf("model_id is required")
	}
	if amount == 0 {
		return CreateEscrowResult{}, fmt.Errorf("amount is required")
	}
	creator := signer.Address()
	have, err := c.transport.SpendableBalance(ctx, creator, EscrowDenom)
	if err != nil {
		return CreateEscrowResult{}, err
	}
	if need := c.createCost(amount); have < need {
		return CreateEscrowResult{}, &WalletUnderfundedError{Address: creator, Have: have, Need: need}
	}
	chainID, err := c.resolveChainID(ctx)
	if err != nil {
		return CreateEscrowResult{}, err
	}
	account, err := c.transport.Account(ctx, creator)
	if err != nil {
		return CreateEscrowResult{}, err
	}
	ttl := c.now().Add(UnorderedTxTTL)
	txBytes, err := buildCreateEscrowTx(signer, chainID, account.Number, c.feeDenom, c.feeAmount, c.gasLimit, amount, modelID, ttl)
	if err != nil {
		return CreateEscrowResult{}, err
	}
	txHash := txHashFromBytes(txBytes)
	if onPrepared != nil {
		if err := onPrepared(txHash); err != nil {
			return CreateEscrowResult{}, fmt.Errorf("record escrow create intent before broadcast: %w", err)
		}
	}
	nodeHash, err := c.transport.Broadcast(ctx, txBytes)
	if err != nil {
		return CreateEscrowResult{}, err
	}
	if !strings.EqualFold(nodeHash, txHash) {
		return CreateEscrowResult{}, fmt.Errorf("tx hash mismatch: precomputed %s, node returned %s", txHash, nodeHash)
	}
	escrowID, err := c.waitForCreatedEscrowID(ctx, txHash)
	if err != nil {
		return CreateEscrowResult{}, err
	}
	return CreateEscrowResult{EscrowID: escrowID, TxHash: txHash, Creator: creator}, nil
}

// createCost is the amount plus the fee when both are paid in the escrow denom.
func (c *TxClient) createCost(amount uint64) uint64 {
	if c.feeDenom != EscrowDenom {
		return amount
	}
	if amount > math.MaxUint64-c.feeAmount {
		return math.MaxUint64
	}
	return amount + c.feeAmount
}

// SettleEscrow builds, signs, broadcasts and confirms a MsgSettleDevshardEscrow tx; unlike CreateEscrow it waits for the commit. See README.md, "Transaction encoding".
func (c *TxClient) SettleEscrow(ctx context.Context, signer *signing.Secp256k1Signer, input SettlementInput, onPrepared func(txHash string) error) (SettleEscrowResult, error) {
	if signer == nil {
		return SettleEscrowResult{}, fmt.Errorf("signer is required")
	}
	if input.EscrowID == 0 {
		return SettleEscrowResult{}, fmt.Errorf("escrow_id is required")
	}
	settler := signer.Address()
	chainID, err := c.resolveChainID(ctx)
	if err != nil {
		return SettleEscrowResult{}, err
	}
	account, err := c.transport.Account(ctx, settler)
	if err != nil {
		return SettleEscrowResult{}, err
	}
	ttl := c.now().Add(UnorderedTxTTL)
	txBytes, err := buildSettleEscrowTx(signer, chainID, account.Number, c.feeDenom, c.feeAmount, c.gasLimit, settler, input, ttl)
	if err != nil {
		return SettleEscrowResult{}, err
	}
	txHash := txHashFromBytes(txBytes)
	if onPrepared != nil {
		if err := onPrepared(txHash); err != nil {
			return SettleEscrowResult{}, fmt.Errorf("record settle intent before broadcast: %w", err)
		}
	}
	nodeHash, err := c.transport.Broadcast(ctx, txBytes)
	if err != nil {
		return SettleEscrowResult{}, err
	}
	if !strings.EqualFold(nodeHash, txHash) {
		return SettleEscrowResult{}, fmt.Errorf("tx hash mismatch: precomputed %s, node returned %s", txHash, nodeHash)
	}
	if c.narrator != nil {
		c.narrator.SettleBroadcast(strconv.FormatUint(input.EscrowID, 10), txHash, settler)
	}
	// Waiting is what makes the result mean "settled": the caller destroys the means to retry.
	if err := c.waitForCommit(ctx, txHash); err != nil {
		return SettleEscrowResult{}, fmt.Errorf("awaiting settlement commit for tx %s: %w", txHash, err)
	}
	return SettleEscrowResult{EscrowID: input.EscrowID, TxHash: txHash, Settler: settler}, nil
}

func (c *TxClient) resolveChainID(ctx context.Context) (string, error) {
	return c.transport.ChainID(ctx)
}

// GetTxEscrowID reports found=false when the tx committed and failed; a transport failure is neither that nor ErrTxNotFound. See README.md, "Transaction encoding".
func (c *TxClient) GetTxEscrowID(ctx context.Context, txHash string) (uint64, bool, error) {
	result, found, err := c.transport.Tx(ctx, txHash)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, ErrTxNotFound
	}
	if result.Code != 0 {
		return 0, false, nil // tx committed but failed -> no escrow created
	}
	if escrowID, held := result.createdEscrowID(); held {
		return escrowID, true, nil
	}
	return 0, false, fmt.Errorf("tx %s committed but escrow_id event was not found", txHash)
}

// TxCommitted reports whether a transaction reached the chain and succeeded there. See README.md, "Transaction encoding".
func (c *TxClient) TxCommitted(ctx context.Context, txHash string) (succeeded bool, err error) {
	result, found, err := c.transport.Tx(ctx, txHash)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrTxNotFound
	}
	return result.Code == 0, nil
}

// errUnconfirmed says what the caller must not conclude. See README.md, "Transaction encoding".
func errUnconfirmed(txHash string, within time.Duration) error {
	return fmt.Errorf("tx %s was broadcast but not confirmed within %s; its commitment is kept and reconciled, so do not create another", txHash, within)
}

// awaitTx polls until a committed tx satisfies ready, pollTimeout elapses, or ctx is done. See README.md, "Transaction encoding".
func (c *TxClient) awaitTx(ctx context.Context, txHash string, ready func(TxResult) bool) (TxResult, error) {
	deadline := c.now().Add(c.pollTimeout)
	var lastErr error
	for {
		result, found, err := c.transport.Tx(ctx, txHash)
		switch {
		case err != nil:
			lastErr = err
		case !found:
			lastErr = fmt.Errorf("tx %s is not visible on chain yet", txHash)
		case result.Code != 0:
			return TxResult{}, fmt.Errorf("tx %s failed code=%d codespace=%s raw_log=%s", txHash, result.Code, result.Codespace, result.RawLog)
		case ready(result):
			return result, nil
		default:
			lastErr = fmt.Errorf("tx %s committed but is not yet complete", txHash)
		}
		if c.now().After(deadline) {
			if lastErr != nil {
				return TxResult{}, fmt.Errorf("%w: %w", errUnconfirmed(txHash, c.pollTimeout), lastErr)
			}
			return TxResult{}, errUnconfirmed(txHash, c.pollTimeout)
		}
		select {
		case <-ctx.Done():
			return TxResult{}, ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

func (c *TxClient) waitForCreatedEscrowID(ctx context.Context, txHash string) (uint64, error) {
	committed, err := c.awaitTx(ctx, txHash, func(response TxResult) bool {
		_, found := response.createdEscrowID()
		return found
	})
	if err != nil {
		return 0, err
	}
	escrowID, _ := committed.createdEscrowID()
	return escrowID, nil
}

// waitForCommit reports that a transaction executed, not merely that it was accepted.
func (c *TxClient) waitForCommit(ctx context.Context, txHash string) error {
	_, err := c.awaitTx(ctx, txHash, func(TxResult) bool { return true })
	return err
}

func (r TxResult) createdEscrowID() (uint64, bool) {
	if id, ok := createdEscrowIDFromEvents(r.Events); ok {
		return id, true
	}
	return 0, false
}

func createdEscrowIDFromEvents(events []TxEvent) (uint64, bool) {
	for _, event := range events {
		if event.Type != "devshard_escrow_created" {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key != "escrow_id" {
				continue
			}
			id, err := strconv.ParseUint(attr.Value, 10, 64)
			if err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}
