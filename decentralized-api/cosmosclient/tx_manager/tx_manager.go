package tx_manager

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/nats/server"
	"decentralized-api/logging"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/tx"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"

	"strings"

	"github.com/nats-io/nats.go"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	v1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	"github.com/productscience/inference/app"
	blstypes "github.com/productscience/inference/x/bls/types"
	collateraltypes "github.com/productscience/inference/x/collateral/types"
	"github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"
)

const (
	txSenderConsumer   = "tx-sender"
	txObserverConsumer = "tx-observer"

	defaultSenderNackDelay   = time.Second * 7
	defaultObserverNackDelay = time.Second * 5

	hashHeader = "TX_HASH"
	idHeader   = "TX_ID"

	maxBlockTimeDrift = 120 * time.Second
)

type TxManager interface {
	SendTransactionAsyncWithRetry(rawTx sdk.Msg, deadlineBlock ...int64) (*sdk.TxResponse, error)
	SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendBatchAsyncWithRetry(msgs []sdk.Msg, deadlineBlock ...int64) error
	SendTransactionSyncNoRetry(msg proto.Message) (*ctypes.ResultTx, error)
	BroadcastMessages(id string, msgs ...sdk.Msg) (*sdk.TxResponse, time.Time, error)
	GetClientContext() client.Context
	GetKeyring() *keyring.Keyring
	GetApiAccount() apiconfig.ApiAccount
	Status(ctx context.Context) (*ctypes.ResultStatus, error)
	BankBalances(ctx context.Context, address string) ([]sdk.Coin, error)
	GetJetStream() nats.JetStreamContext
}

type blockTimeTracker struct {
	latestBlockTime time.Time
	lastUpdatedAt   time.Time
	maxBlockTimeout time.Duration
	pauseSending    bool
	mtx             sync.Mutex
}

type manager struct {
	ctx              context.Context
	client           *cosmosclient.Client
	apiAccount       *apiconfig.ApiAccount
	txFactory        *tx.Factory
	accountRetriever client.AccountRetriever
	address          string
	defaultTimeout   time.Duration
	natsConnection   *nats.Conn
	natsJetStream    nats.JetStreamContext
	blockTimeTracker *blockTimeTracker
	getHeightFunc    func() int64
}

func StartTxManager(
	ctx context.Context,
	client *cosmosclient.Client,
	account *apiconfig.ApiAccount,
	defaultTimeout time.Duration,
	natsConnection *nats.Conn,
	address string,
	getHeight func() int64) (*manager, error) {
	js, err := natsConnection.JetStream()
	if err != nil {
		return nil, err
	}

	// Register all module interfaces to match admin server codec
	app.RegisterLegacyModules(client.Context().InterfaceRegistry)
	types.RegisterInterfaces(client.Context().InterfaceRegistry)
	banktypes.RegisterInterfaces(client.Context().InterfaceRegistry)
	v1.RegisterInterfaces(client.Context().InterfaceRegistry)
	upgradetypes.RegisterInterfaces(client.Context().InterfaceRegistry)
	collateraltypes.RegisterInterfaces(client.Context().InterfaceRegistry)
	restrictionstypes.RegisterInterfaces(client.Context().InterfaceRegistry)
	blstypes.RegisterInterfaces(client.Context().InterfaceRegistry)

	m := &manager{
		ctx:              ctx,
		client:           client,
		address:          address,
		apiAccount:       account,
		accountRetriever: authtypes.AccountRetriever{},
		defaultTimeout:   defaultTimeout,
		natsConnection:   natsConnection,
		natsJetStream:    js,
		getHeightFunc:    getHeight,
		blockTimeTracker: &blockTimeTracker{
			maxBlockTimeout: 10 * time.Second,
		},
	}
	if err := m.sendTxs(); err != nil {
		return nil, err
	}

	if err := m.observeTxs(); err != nil {
		return nil, err
	}

	return m, nil
}

const maxAttempts = 100

func getJitteredDelay(base time.Duration) time.Duration {
	jitterFactor := 0.7 + rand.Float64()*0.6
	return time.Duration(float64(base) * jitterFactor)
}

type txToSend struct {
	TxInfo      txInfo
	Sent        bool
	Attempts    int
	RequeueTime time.Time `json:",omitempty"`
}

type txInfo struct {
	Id            string
	RawTx         []byte
	RawBatch      [][]byte
	TxHash        string
	Timeout       time.Time
	Attempts      int
	DeadlineBlock int64 `json:",omitempty"` // Block after which tx is stale
}

func (t *txInfo) IsBatch() bool {
	return len(t.RawBatch) > 0
}

func (m *manager) GetApiAccount() apiconfig.ApiAccount {
	return *m.apiAccount
}

func (m *manager) Status(ctx context.Context) (*ctypes.ResultStatus, error) {
	return m.client.Status(ctx)
}

func (m *manager) SendTransactionAsyncWithRetry(rawTx sdk.Msg, deadlineBlockOpt ...int64) (*sdk.TxResponse, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionAsyncWithRetry: sending tx", types.Messages, "tx_id", id)

	var deadlineBlock int64
	if len(deadlineBlockOpt) > 0 && deadlineBlockOpt[0] > 0 {
		deadlineBlock = deadlineBlockOpt[0]
	} else {
		msgType := sdk.MsgTypeURL(rawTx)
		deadlineBlock = m.getLatestBlockHeight() + getMaxBlocksForType(msgType)
	}

	if halt, err := m.updateChainHalt(); err != nil || halt {
		logging.Error("chain is slowing down or couldn't fetch actual chain status", types.Messages, "latest_block_timestamp", m.blockTimeTracker.latestBlockTime)

		if err := m.putOnRetry(id, "", time.Time{}, rawTx, 0, false, deadlineBlock); err != nil {
			logging.Error("failed to put in queue", types.Messages, "tx_id", id, "resend_err", err)
			return nil, ErrTxFailedToBroadcastAndPutOnRetry
		}
		return &sdk.TxResponse{}, nil
	}

	resp, timeout, broadcastErr := m.broadcastMessage(id, rawTx)
	if broadcastErr != nil {
		// Check if broadcast error is retryable
		if isRetryableBroadcastError(broadcastErr) {
			if err := m.putOnRetry(id, "", timeout, rawTx, 1, false, deadlineBlock); err != nil {
				logging.Error("tx failed to broadcast, failed to put in queue", types.Messages, "tx_id", id, "broadcast_err", broadcastErr, "resend_err", err)
			}
			return nil, ErrTxFailedToBroadcastAndPutOnRetry
		}
		// Non-retryable broadcast error - fail immediately
		logging.Error("SendTransactionAsyncWithRetry: non-retryable broadcast error", types.Messages, "tx_id", id, "err", broadcastErr)
		return nil, broadcastErr
	}

	// Classify the response to determine action
	action := classifyBroadcastResponse(resp)
	switch action {
	case TxActionFail:
		logging.Warn("Non-retryable business error, failing immediately", types.Messages,
			"tx_id", id, "code", resp.Code, "codespace", resp.Codespace, "rawLog", resp.RawLog)
		return nil, NewTransactionErrorFromResponse(resp)
	case TxActionRetry:
		logging.Warn("Retryable response error, queuing for retry", types.Messages,
			"tx_id", id, "code", resp.Code, "rawLog", resp.RawLog)
		if err := m.putOnRetry(id, "", timeout, rawTx, 1, false, deadlineBlock); err != nil {
			logging.Error("tx failed, failed to put in queue for retry", types.Messages, "tx_id", id, "err", err)
		}
		return nil, ErrTxFailedToBroadcastAndPutOnRetry
	case TxActionObserve:
		// Success or tx-in-mempool - queue for observation
		if err := m.putOnRetry(id, resp.TxHash, timeout, rawTx, 1, true, deadlineBlock); err != nil {
			logging.Error("tx broadcast, but failed to put in queue", types.Messages, "tx_id", id, "err", err)
		}
		return resp, nil
	}

	// Should never reach here, but fail safe
	logging.Error("Unexpected classification result", types.Messages, "tx_id", id)
	return nil, ErrTxFailedToBroadcastAndPutOnRetry
}

func (m *manager) SendBatchAsyncWithRetry(msgs []sdk.Msg, deadlineBlockOpt ...int64) error {
	if len(msgs) == 0 {
		return nil
	}

	var deadlineBlock int64
	if len(deadlineBlockOpt) > 0 && deadlineBlockOpt[0] > 0 {
		deadlineBlock = deadlineBlockOpt[0]
	} else {
		var minBlocks int64 = defaultMaxBlocks
		for _, msg := range msgs {
			if blocks := getMaxBlocksForType(sdk.MsgTypeURL(msg)); blocks < minBlocks {
				minBlocks = blocks
			}
		}
		deadlineBlock = m.getLatestBlockHeight() + minBlocks
	}

	if len(msgs) == 1 {
		_, err := m.SendTransactionAsyncWithRetry(msgs[0], deadlineBlock)
		return err
	}

	id := uuid.New().String()
	logging.Debug("SendBatchAsyncWithRetry: sending batch", types.Messages, "tx_id", id, "count", len(msgs))

	if halt, err := m.updateChainHalt(); err != nil || halt {
		logging.Error("chain is slowing down or couldn't fetch actual chain status", types.Messages, "latest_block_timestamp", m.blockTimeTracker.latestBlockTime)

		if err := m.putBatchOnRetry(id, msgs, "", time.Time{}, 0, false, deadlineBlock); err != nil {
			logging.Error("failed to put batch in queue", types.Messages, "tx_id", id, "resend_err", err)
			return ErrTxFailedToBroadcastAndPutOnRetry
		}
		return nil
	}

	resp, timeout, broadcastErr := m.BroadcastMessages(id, msgs...)
	if broadcastErr != nil {
		// Check if broadcast error is retryable
		if isRetryableBroadcastError(broadcastErr) {
			if err := m.putBatchOnRetry(id, msgs, "", timeout, 1, false, deadlineBlock); err != nil {
				logging.Error("batch failed to broadcast, failed to put in queue", types.Messages, "tx_id", id, "broadcast_err", broadcastErr, "resend_err", err)
			}
			return ErrTxFailedToBroadcastAndPutOnRetry
		}
		// Non-retryable broadcast error - fail immediately
		logging.Error("SendBatchAsyncWithRetry: non-retryable broadcast error", types.Messages, "tx_id", id, "err", broadcastErr)
		return broadcastErr
	}

	// Classify the response to determine action
	action := classifyBroadcastResponse(resp)
	switch action {
	case TxActionFail:
		logging.Warn("Non-retryable business error in batch, failing immediately", types.Messages,
			"tx_id", id, "code", resp.Code, "codespace", resp.Codespace, "rawLog", resp.RawLog)
		return NewTransactionErrorFromResponse(resp)
	case TxActionRetry:
		logging.Warn("Retryable response error in batch, queuing for retry", types.Messages,
			"tx_id", id, "code", resp.Code, "rawLog", resp.RawLog)
		if err := m.putBatchOnRetry(id, msgs, "", timeout, 1, false, deadlineBlock); err != nil {
			logging.Error("batch failed, failed to put in queue for retry", types.Messages, "tx_id", id, "err", err)
		}
		return ErrTxFailedToBroadcastAndPutOnRetry
	case TxActionObserve:
		// Success or tx-in-mempool - queue for observation
		if err := m.putBatchOnRetry(id, msgs, resp.TxHash, timeout, 1, true, deadlineBlock); err != nil {
			logging.Error("batch broadcast, but failed to put in queue", types.Messages, "tx_id", id, "err", err)
		}
		return nil
	}

	// Should never reach here, but fail safe
	logging.Error("Unexpected classification result for batch", types.Messages, "tx_id", id)
	return ErrTxFailedToBroadcastAndPutOnRetry
}

func (m *manager) SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionAsyncNoRetry: sending tx", types.Messages, "tx_id", id, "originalMsgType", sdk.MsgTypeURL(rawTx))
	_, err := m.updateChainHalt()
	if err != nil {
		return nil, err
	}
	resp, _, broadcastErr := m.broadcastMessage(id, rawTx)
	return resp, broadcastErr
}

func (m *manager) SendTransactionSyncNoRetry(msg proto.Message) (*ctypes.ResultTx, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionSyncNoRetry: sending tx", types.Messages, "tx_id", id)
	_, err := m.updateChainHalt()
	if err != nil {
		return nil, err
	}
	resp, _, err := m.broadcastMessage(id, msg)
	if err != nil {
		return nil, err
	}

	logging.Debug("Transaction broadcast successful", types.Messages, "tx_id", id, "tx_hash", resp.TxHash)
	result, err := m.WaitForResponse(resp.TxHash)
	if err != nil {
		logging.Error("Failed to wait for transaction", types.Messages, "tx_id", id, "tx_hash", resp.TxHash, "error", err)
		return nil, err
	}
	return result, nil
}

func (m *manager) GetKeyring() *keyring.Keyring {
	return &m.client.AccountRegistry.Keyring
}

func (m *manager) putOnRetry(
	id,
	txHash string,
	timeout time.Time,
	rawTx sdk.Msg,
	attempts int,
	sent bool,
	deadlineBlock int64,
) error {
	logging.Debug("putOnRetry: tx with params", types.Messages,
		"tx_id", id,
		"tx_hash", txHash,
		"timeout", timeout.String(),
		"sent", sent,
		"deadlineBlock", deadlineBlock,
	)

	if attempts >= maxAttempts {
		logging.Warn("tx reached max attempts", types.Messages, "tx_id", id)
		return nil
	}

	bz, err := m.client.Context().Codec.MarshalInterfaceJSON(rawTx)
	if err != nil {
		return err
	}

	if id == "" {
		id = uuid.New().String()
	}

	b, err := json.Marshal(&txToSend{
		TxInfo: txInfo{
			Id:            id,
			RawTx:         bz,
			TxHash:        txHash,
			Timeout:       timeout,
			DeadlineBlock: deadlineBlock,
		},
		Sent:     sent,
		Attempts: attempts,
	})
	if err != nil {
		return err
	}
	msg := &nats.Msg{Subject: server.TxsToSendStream, Data: b, Header: nats.Header{}}
	msg.Header.Set(idHeader, id)
	msg.Header.Set(hashHeader, txHash)
	_, err = m.natsJetStream.PublishMsg(msg)
	return err
}

func (m *manager) putBatchOnRetry(
	id string,
	msgs []sdk.Msg,
	txHash string,
	timeout time.Time,
	attempts int,
	sent bool,
	deadlineBlock int64,
) error {
	logging.Debug("putBatchOnRetry: batch with params", types.Messages,
		"tx_id", id,
		"tx_hash", txHash,
		"timeout", timeout.String(),
		"sent", sent,
		"count", len(msgs),
		"deadlineBlock", deadlineBlock,
	)

	if attempts >= maxAttempts {
		logging.Warn("batch tx reached max attempts", types.Messages, "tx_id", id)
		return nil
	}

	rawBatch := make([][]byte, len(msgs))
	for i, msg := range msgs {
		bz, err := m.client.Context().Codec.MarshalInterfaceJSON(msg)
		if err != nil {
			return err
		}
		rawBatch[i] = bz
	}

	if id == "" {
		id = uuid.New().String()
	}

	b, err := json.Marshal(&txToSend{
		TxInfo: txInfo{
			Id:            id,
			RawBatch:      rawBatch,
			TxHash:        txHash,
			Timeout:       timeout,
			DeadlineBlock: deadlineBlock,
		},
		Sent:     sent,
		Attempts: attempts,
	})
	if err != nil {
		return err
	}
	msg := &nats.Msg{Subject: server.TxsToSendStream, Data: b, Header: nats.Header{}}
	msg.Header.Set(idHeader, id)
	msg.Header.Set(hashHeader, txHash)
	_, err = m.natsJetStream.PublishMsg(msg)
	return err
}

func (m *manager) putInfoToObserve(info txInfo) error {
	logging.Debug("putInfoToObserve: tx with params", types.Messages,
		"tx_id", info.Id,
		"tx_hash", info.TxHash,
		"timeout", info.Timeout.String(),
	)

	b, err := json.Marshal(&info)
	if err != nil {
		return err
	}
	msg := &nats.Msg{Subject: server.TxsToObserveStream, Data: b, Header: nats.Header{}}
	msg.Header.Set(idHeader, info.Id)
	msg.Header.Set(hashHeader, info.TxHash)
	_, err = m.natsJetStream.PublishMsg(msg)
	return err
}

func (m *manager) requeue(tx *txToSend) error {
	tx.Attempts++
	tx.RequeueTime = time.Now()
	if tx.Attempts >= maxAttempts {
		logging.Warn("tx max attempts reached", types.Messages, "id", tx.TxInfo.Id)
		return nil
	}
	b, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	msg := &nats.Msg{Subject: server.TxsToSendStream, Data: b, Header: nats.Header{}}
	msg.Header.Set(idHeader, tx.TxInfo.Id)
	msg.Header.Set(hashHeader, tx.TxInfo.TxHash)
	_, err = m.natsJetStream.PublishMsg(msg)
	return err
}

func (m *manager) sendTxs() error {
	logging.Info("Tx manager: sending txs: run in background", types.Messages)

	_, err := m.natsJetStream.Subscribe(server.TxsToSendStream, func(msg *nats.Msg) {
		if halt, _ := m.updateChainHalt(); halt {
			logging.Warn("node paused, delaying tx", types.Messages,
				"latest_block_timestamp", m.blockTimeTracker.latestBlockTime)
			msg.NakWithDelay(getJitteredDelay(defaultSenderNackDelay))
			return
		}

		txId := msg.Header.Get(idHeader)
		txHash := msg.Header.Get(hashHeader)
		logging.Debug("sendTxs processing", types.Messages, "id", txId, "hash", txHash)

		var tx txToSend
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			logging.Error("error unmarshaling tx_to_send", types.Messages, "err", err, "id", txId, "hash", txHash)
			msg.Term() // malformed, drop it
			return
		}

		logging.Debug("SendTxs: got tx", types.Messages, "id", tx.TxInfo.Id, "attempts", tx.Attempts)

		if tx.Attempts >= maxAttempts {
			logging.Warn("tx max attempts reached", types.Messages, "id", tx.TxInfo.Id)
			msg.Term()
			return
		}

		if !tx.RequeueTime.IsZero() {
			elapsed := time.Since(tx.RequeueTime)
			jitteredDelay := getJitteredDelay(defaultSenderNackDelay)
			if elapsed < jitteredDelay {
				msg.NakWithDelay(jitteredDelay - elapsed)
				return
			}
		}

		currentHeight := m.getLatestBlockHeight()
		if tx.TxInfo.DeadlineBlock > 0 && currentHeight > tx.TxInfo.DeadlineBlock {
			logging.Warn("tx expired by block deadline, dropping", types.Messages,
				"id", tx.TxInfo.Id,
				"hash", tx.TxInfo.TxHash,
				"deadline", tx.TxInfo.DeadlineBlock,
				"currentHeight", currentHeight)
			msg.Term()
			return
		}

		var resp *sdk.TxResponse
		var timeout time.Time
		var broadcastErr error

		if tx.TxInfo.IsBatch() {
			msgs, err := m.unpackBatch(tx.TxInfo.RawBatch)
			if err != nil {
				logging.Error("error unpacking batch", types.Messages, "id", tx.TxInfo.Id, "err", err)
				msg.Term()
				return
			}

			if !tx.Sent {
				logging.Debug("start broadcast batch async", types.Messages, "id", tx.TxInfo.Id)
				resp, timeout, broadcastErr = m.BroadcastMessages(tx.TxInfo.Id, msgs...)
			}
		} else {
			rawTx, err := m.unpackTx(tx.TxInfo.RawTx)
			if err != nil {
				logging.Error("error unpacking raw tx", types.Messages, "id", tx.TxInfo.Id, "err", err)
				msg.Term() // malformed, drop it
				return
			}

			if !tx.Sent {
				logging.Debug("start broadcast tx async", types.Messages, "id", tx.TxInfo.Id)
				resp, timeout, broadcastErr = m.broadcastMessage(tx.TxInfo.Id, rawTx)
			}
		}

		if !tx.Sent {
			if broadcastErr != nil {
				// Check if broadcast error is retryable
				if isRetryableBroadcastError(broadcastErr) {
					logging.Warn("retryable broadcast error, requeuing", types.Messages, "id", tx.TxInfo.Id, "err", broadcastErr)
					if err := m.requeue(&tx); err != nil {
						logging.Error("requeue failed, dropping tx", types.Messages, "id", tx.TxInfo.Id, "err", err)
					}
					msg.Ack()
					return
				}
				// Non-retryable broadcast error - drop permanently
				logging.Error("non-retryable broadcast error in sendTxs, dropping", types.Messages, "id", tx.TxInfo.Id, "err", broadcastErr)
				msg.Term()
				return
			}

			// Classify the response to determine action
			action := classifyBroadcastResponse(resp)
			switch action {
			case TxActionFail:
				logging.Warn("Non-retryable business error in sendTxs, dropping", types.Messages,
					"id", tx.TxInfo.Id, "code", resp.Code, "codespace", resp.Codespace, "rawLog", resp.RawLog)
				msg.Term()
				return
			case TxActionRetry:
				logging.Warn("Retryable response error, requeuing", types.Messages,
					"id", tx.TxInfo.Id, "code", resp.Code, "rawLog", resp.RawLog)
				if err := m.requeue(&tx); err != nil {
					logging.Error("requeue failed, dropping tx", types.Messages, "id", tx.TxInfo.Id, "err", err)
				}
				msg.Ack()
				return
			case TxActionObserve:
				// Success or tx-in-mempool - continue to observer
				tx.TxInfo.Timeout = timeout
				tx.TxInfo.TxHash = resp.TxHash
				tx.Sent = true
			}
		}

		logging.Debug("tx broadcast, put to observe", types.Messages, "id", tx.TxInfo.Id, "tx_hash", tx.TxInfo.TxHash, "timeout", tx.TxInfo.Timeout.String())

		if err := m.putInfoToObserve(tx.TxInfo); err != nil {
			logging.Error("error pushing to observe queue, tx broadcast but untracked",
				types.Messages, "id", tx.TxInfo.Id, "txHash", tx.TxInfo.TxHash, "err", err)
		}
		msg.Ack()
	}, nats.Durable(txSenderConsumer), nats.ManualAck())
	return err
}

func (m *manager) observeTxs() error {
	logging.Info("Tx manager: observeTxs txs: run in background", types.Messages)
	_, err := m.natsJetStream.Subscribe(server.TxsToObserveStream, func(msg *nats.Msg) {
		if halt, _ := m.updateChainHalt(); halt {
			logging.Warn("node paused, delaying observe", types.Messages,
				"latest_block_timestamp", m.blockTimeTracker.latestBlockTime)
			msg.NakWithDelay(defaultObserverNackDelay)
			return
		}

		var tx txInfo
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			logging.Error("error unmarshaling tx_to_observe", types.Messages, "err", err)
			msg.Term()
			return
		}

		currentHeight := m.getLatestBlockHeight()
		if tx.DeadlineBlock > 0 && currentHeight > tx.DeadlineBlock {
			logging.Warn("tx expired by block deadline in observer, dropping", types.Messages,
				"id", tx.Id,
				"deadline", tx.DeadlineBlock,
				"currentHeight", currentHeight)
			msg.Term()
			return
		}

		var rawTx sdk.Msg
		var msgs []sdk.Msg
		var err error

		if tx.IsBatch() {
			msgs, err = m.unpackBatch(tx.RawBatch)
		} else {
			rawTx, err = m.unpackTx(tx.RawTx)
		}

		if err != nil {
			msg.Term()
			return
		}

		if tx.TxHash == "" {
			logging.Warn("tx hash is empty", types.Messages, "tx_id", tx.Id)

			tx.Attempts++
			var retryErr error
			if tx.IsBatch() {
				retryErr = m.putBatchOnRetry(tx.Id, msgs, "", time.Time{}, tx.Attempts, false, tx.DeadlineBlock)
			} else {
				retryErr = m.putOnRetry(tx.Id, "", time.Time{}, rawTx, tx.Attempts, false, tx.DeadlineBlock)
			}

			if retryErr != nil {
				msg.NakWithDelay(defaultObserverNackDelay)
				return
			}
			msg.Ack()
			return
		}

		found, err := m.checkTxStatus(tx.TxHash)
		if found {
			logging.Debug("tx found, remove tx from observer queue", types.Messages, "tx_id", tx.Id, "txHash", tx.TxHash)
			if err := msg.Ack(); err != nil {
				logging.Error("ack error", types.Messages, "tx_id", tx.Id, "err", err)
			}
			return
		}

		if errors.Is(err, ErrDecodingTxHash) {
			msg.Term()
			return
		}

		if errors.Is(err, ErrTxNotFound) {
			if m.blockTimeTracker.latestBlockTime.After(tx.Timeout) {
				logging.Debug("tx expired", types.Messages, "tx_id", tx.Id, "tx_hash", tx.TxHash, "tx_timestamp", tx.Timeout, "latest_block_timestamp", m.blockTimeTracker.latestBlockTime)
				tx.Attempts++

				var retryErr error
				if tx.IsBatch() {
					retryErr = m.putBatchOnRetry(tx.Id, msgs, "", time.Time{}, tx.Attempts, false, tx.DeadlineBlock)
				} else {
					retryErr = m.putOnRetry(tx.Id, "", time.Time{}, rawTx, tx.Attempts, false, tx.DeadlineBlock)
				}

				if retryErr != nil {
					msg.NakWithDelay(defaultObserverNackDelay)
					return
				}
				msg.Ack()
				return
			}
		}

		// Likely: The tx is not (yet) found, and the tx hasn't expired
		msg.NakWithDelay(defaultObserverNackDelay)
	}, nats.Durable(txObserverConsumer), nats.ManualAck())
	return err
}

func (m *manager) GetClientContext() client.Context {
	return m.client.Context()
}

func (m *manager) checkTxStatus(hash string) (bool, error) {
	bz, err := hex.DecodeString(hash)
	if err != nil {
		logging.Error("checkTxStatus: error decoding tx hash", types.Messages, "err", err)
		return false, ErrDecodingTxHash
	}

	resp, err := m.client.Context().Client.Tx(m.ctx, bz, false)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, ErrTxNotFound
		}
		return false, err
	}

	if resp.TxResult.Code != 0 {
		logging.Error("checkTxStatus: tx failed on-chain", types.Messages, "txHash", hash, "code", resp.TxResult.Code, "codespace", resp.TxResult.Codespace, "rawLog", resp.TxResult.Log)
	}
	logging.Debug("checkTxStatus: found tx result", types.Messages, "txHash", hash, "resp", resp)
	return true, nil
}

func (m *manager) WaitForResponse(txHash string) (*ctypes.ResultTx, error) {
	ctx, cancel := context.WithTimeout(m.ctx, time.Second*15)
	defer cancel()

	transactionAppliedResult, err := m.client.WaitForTx(ctx, txHash)
	if err != nil {
		logging.Error("Failed to wait for transaction", types.Messages, "error", err, "result", transactionAppliedResult)
		return nil, err
	}

	txResult := transactionAppliedResult.TxResult
	if txResult.Code != 0 {
		logging.Error("Transaction failed on-chain", types.Messages, "txHash", txHash, "code", txResult.Code, "codespace", txResult.Codespace, "rawLog", txResult.Log)
		return nil, NewTransactionErrorFromResult(transactionAppliedResult)
	}
	return transactionAppliedResult, nil
}

func (m *manager) BankBalances(ctx context.Context, address string) ([]sdk.Coin, error) {
	return m.client.BankBalances(ctx, address, nil)
}

func (m *manager) GetJetStream() nats.JetStreamContext {
	return m.natsJetStream
}

func (m *manager) BroadcastMessages(id string, msgs ...sdk.Msg) (*sdk.TxResponse, time.Time, error) {
	if len(msgs) == 0 {
		return nil, time.Time{}, nil
	}
	if len(msgs) == 1 {
		return m.broadcastMessage(id, msgs[0])
	}

	factory, err := m.getFactory(id)
	if err != nil {
		return nil, time.Time{}, err
	}

	var finalMsgs []sdk.Msg
	if !m.apiAccount.IsSignerTheMainAccount() {
		granteeAddress, err := m.apiAccount.SignerAddress()
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("failed to get signer address: %w", err)
		}
		execMsg := authztypes.NewMsgExec(granteeAddress, msgs)
		finalMsgs = []sdk.Msg{&execMsg}
		logging.Debug("Using authz MsgExec for batch", types.Messages, "grantee", granteeAddress.String(), "msgCount", len(msgs))
	} else {
		finalMsgs = msgs
	}

	unsignedTx, err := factory.BuildUnsignedTx(finalMsgs...)
	if err != nil {
		return nil, time.Time{}, err
	}
	txBytes, timestamp, err := m.getSignedBytes(id, unsignedTx, factory)
	if err != nil {
		return nil, time.Time{}, err
	}

	resp, err := m.client.Context().BroadcastTxSync(txBytes)
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.Code != 0 {
		logging.Error("Batch broadcast failed", types.Messages, "code", resp.Code, "rawLog", resp.RawLog, "tx_id", id, "msgCount", len(msgs))
	} else {
		logging.Debug("Batch broadcast successful", types.Messages, "tx_id", id, "msgCount", len(msgs))
	}
	return resp, timestamp, nil
}

func (m *manager) broadcastMessage(id string, rawTx sdk.Msg) (*sdk.TxResponse, time.Time, error) {
	factory, err := m.getFactory(id)
	if err != nil {
		return nil, time.Time{}, err
	}

	var finalMsg sdk.Msg = rawTx
	originalMsgType := sdk.MsgTypeURL(rawTx)
	if !m.apiAccount.IsSignerTheMainAccount() {
		granteeAddress, err := m.apiAccount.SignerAddress()
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("failed to get signer address: %w", err)
		}

		execMsg := authztypes.NewMsgExec(granteeAddress, []sdk.Msg{rawTx})
		finalMsg = &execMsg
		logging.Debug("Using authz MsgExec", types.Messages, "grantee", granteeAddress.String(), "originalMsgType", originalMsgType)
	}

	unsignedTx, err := factory.BuildUnsignedTx(finalMsg)
	if err != nil {
		return nil, time.Time{}, err
	}
	txBytes, timestamp, err := m.getSignedBytes(id, unsignedTx, factory)
	if err != nil {
		return nil, time.Time{}, err
	}

	resp, err := m.client.Context().BroadcastTxSync(txBytes)
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.Code != 0 {
		logging.Error("Broadcast failed immediately", types.Messages, "code", resp.Code, "rawLog", resp.RawLog, "tx_id", id, "originalMsgType", originalMsgType)
	} else {
		logging.Debug("Broadcast successful", types.Messages, "tx_id", id, "originalMsgType", originalMsgType, "resp", resp)
	}
	return resp, timestamp, nil
}

func (m *manager) unpackTx(bz []byte) (sdk.Msg, error) {
	var unpackedAny codectypes.Any
	if err := m.client.Context().Codec.UnmarshalJSON(bz, &unpackedAny); err != nil {
		return nil, err
	}

	var rawTx sdk.Msg
	if err := m.client.Context().Codec.UnpackAny(&unpackedAny, &rawTx); err != nil {
		return nil, err
	}
	return rawTx, nil
}

func (m *manager) unpackBatch(rawBatch [][]byte) ([]sdk.Msg, error) {
	msgs := make([]sdk.Msg, 0, len(rawBatch))
	for i, bz := range rawBatch {
		msg, err := m.unpackTx(bz)
		if err != nil {
			logging.Error("skipping invalid message in batch", types.Messages, "index", i, "error", err)
			continue
		}
		msgs = append(msgs, msg)
	}
	if len(msgs) == 0 {
		return nil, errors.New("all messages in batch failed to unpack")
	}
	return msgs, nil
}

func (m *manager) getFactory(id string) (*tx.Factory, error) {
	// Now that we don't need the sequence, we only need to create the factory if it doesn't exist
	if m.txFactory != nil {
		return m.txFactory, nil
	}
	address, err := m.apiAccount.SignerAddress()
	if err != nil {
		logging.Error("Failed to get account address", types.Messages, "tx_id", id, "error", err)
		return nil, err
	}
	accountNumber, _, err := m.accountRetriever.GetAccountNumberSequence(m.client.Context(), address)
	if err != nil {
		logging.Error("Failed to get account number and sequence", types.Messages, "tx_id", id, "error", err)
		return nil, err
	}
	factory := m.client.TxFactory.
		WithAccountNumber(accountNumber).
		WithGasAdjustment(10).
		WithFees("").
		WithGasPrices("").
		WithGas(0).
		WithUnordered(true).
		WithKeybase(*m.GetKeyring())
	m.txFactory = &factory
	return &factory, nil
}

func (m *manager) getSignedBytes(id string, unsignedTx client.TxBuilder, factory *tx.Factory) ([]byte, time.Time, error) {
	blockTs := m.blockTimeTracker.latestBlockTime
	if blockTs.IsZero() {
		_, err := m.updateChainHalt()
		if err != nil {
			return nil, time.Time{}, err
		}
		blockTs = m.blockTimeTracker.latestBlockTime
	}

	timestamp := getTimestamp(blockTs.UnixNano(), m.defaultTimeout)

	// Gas is not charged, but without a high gas limit the transactions fail
	unsignedTx.SetGasLimit(10000000000000)
	unsignedTx.SetFeeAmount(sdk.Coins{})
	unsignedTx.SetUnordered(true)
	unsignedTx.SetTimeoutTimestamp(timestamp)
	name := m.apiAccount.SignerAccount.Name
	logging.Debug("Signing transaction", types.Messages, "tx_id", id, "timeout", timestamp.String(), "name", name)

	err := tx.Sign(m.ctx, *factory, name, unsignedTx, false)
	if err != nil {
		logging.Error("Failed to sign transaction", types.Messages, "tx_id", id, "error", err)
		return nil, time.Time{}, err
	}
	txBytes, err := m.client.Context().TxConfig.TxEncoder()(unsignedTx.GetTx())
	if err != nil {
		logging.Error("Failed to encode transaction", types.Messages, "tx_id", id, "error", err)
		return nil, time.Time{}, err
	}
	return txBytes, timestamp, nil
}

func (m *manager) getLatestBlockHeight() int64 {
	return m.getHeightFunc()
}

func (m *manager) isNodeBehind(syncInfo ctypes.SyncInfo) bool {
	if syncInfo.CatchingUp {
		logging.Warn("node is catching up", types.Messages,
			"height", syncInfo.LatestBlockHeight)
		return true
	}

	drift := time.Since(syncInfo.LatestBlockTime)
	if drift > maxBlockTimeDrift {
		logging.Warn("node block time is stale", types.Messages,
			"latestBlockTime", syncInfo.LatestBlockTime,
			"drift", drift)
		return true
	}

	return false
}

func (m *manager) updateChainHalt() (bool, error) {
	m.blockTimeTracker.mtx.Lock()
	now := time.Now()
	if now.Sub(m.blockTimeTracker.lastUpdatedAt) < time.Second*3 {
		result := m.blockTimeTracker.pauseSending
		m.blockTimeTracker.mtx.Unlock()
		return result, nil
	}
	m.blockTimeTracker.mtx.Unlock()

	status, err := m.client.Status(m.ctx)
	if err != nil {
		logging.Error("error getting blockchain status", types.Messages, "err", err)
		return false, err
	}

	m.blockTimeTracker.mtx.Lock()
	defer m.blockTimeTracker.mtx.Unlock()

	// Priority 1: Chain halt detection (chain stopped producing blocks)
	if status.SyncInfo.LatestBlockTime.Equal(m.blockTimeTracker.latestBlockTime) &&
		!m.blockTimeTracker.lastUpdatedAt.IsZero() && now.Sub(m.blockTimeTracker.lastUpdatedAt) > m.blockTimeTracker.maxBlockTimeout {
		m.blockTimeTracker.pauseSending = true
		m.blockTimeTracker.lastUpdatedAt = now
		return true, nil
	}

	// Priority 2: Node behind (catching up or stale block time)
	if m.isNodeBehind(status.SyncInfo) {
		m.blockTimeTracker.latestBlockTime = status.SyncInfo.LatestBlockTime
		m.blockTimeTracker.pauseSending = true
		m.blockTimeTracker.lastUpdatedAt = now
		return true, nil
	}

	// Recovery: passed both checks, safe to send
	m.blockTimeTracker.pauseSending = false

	// Update block time for halt detection
	if status.SyncInfo.LatestBlockTime.After(m.blockTimeTracker.latestBlockTime) {
		m.blockTimeTracker.latestBlockTime = status.SyncInfo.LatestBlockTime
	}

	m.blockTimeTracker.lastUpdatedAt = now
	return m.blockTimeTracker.pauseSending, nil
}
