package cosmosclient

import (
	"context"
	"decentralized-api/internal/nats/server"
	"decentralized-api/logging"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	txclient "github.com/cosmos/cosmos-sdk/client/tx"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/x/inference/types"
	"strings"
)

const (
	txSenderConsumer   = "tx-sender"
	txObserverConsumer = "tx-observer"

	defaultBlockTimeout      = uint64(300) // around 30 mins if block is produced every 5-6 sec
	defaultSenderNackDelay   = time.Second * 7
	defaultObserverNackDelay = time.Second * 1
)

type TxManager interface {
	GetClientContext() client.Context
	SignBytes(seed []byte) ([]byte, error)
	Status(ctx context.Context) (*ctypes.ResultStatus, error)
	SendTransactionAsyncWithRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendTransactionSyncNoRetry(msg proto.Message) (*ctypes.ResultTx, error)
	BankBalances(ctx context.Context, address string) ([]sdk.Coin, error)
	SendTxs() error
	ObserveTxs() error
}

type manager struct {
	client           *cosmosclient.Client
	account          *cosmosaccount.Account
	accountRetriever client.AccountRetriever
	address          string
	highestSequence  int64
	mtx              *sync.Mutex
	blockTimeout     uint64
	nc               *nats.Conn
	js               nats.JetStreamContext
	ctx              context.Context
	paused           bool
}

func NewTxManager(
	ctx context.Context,
	client *cosmosclient.Client,
	account *cosmosaccount.Account,
	accountRetriever client.AccountRetriever,
	nc *nats.Conn,
	address string, blockTimeout uint64) (*manager, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, err
	}

	types.RegisterInterfaces(client.Context().InterfaceRegistry)

	return &manager{
		ctx:              ctx,
		client:           client,
		address:          address,
		account:          account,
		highestSequence:  -1,
		accountRetriever: accountRetriever,
		mtx:              &sync.Mutex{},
		blockTimeout:     blockTimeout,
		nc:               nc,
		js:               js,
	}, nil
}

type txToSend struct {
	TxInfo txInfo
	Sent   bool
}

type txInfo struct {
	Id            string
	RawTx         []byte
	TxHash        string
	Sequence      uint64
	TimeOutHeight uint64
}

func (m *manager) Status(ctx context.Context) (*ctypes.ResultStatus, error) {
	return m.client.Status(ctx)
}

func (m *manager) SendTransactionAsyncWithRetry(rawTx sdk.Msg) (*sdk.TxResponse, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionAsyncWithRetry: sending tx", types.Messages, "tx_id", id)
	resp, sequence, timeout, broadcastErr := m.broadcastMessage(id, rawTx)
	if broadcastErr != nil {
		if isTxErrorCritical(broadcastErr) {
			logging.Debug("SendTransactionAsyncWithRetry: critical error sending tx", types.Messages, "tx_id", id)
			return nil, broadcastErr
		}

		err := m.putOnRetry(id, "", 0, 0, rawTx, false)
		return nil, err
	}
	err := m.putOnRetry(id, resp.TxHash, sequence, timeout, rawTx, true)
	return resp, err
}

func (m *manager) SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionAsyncNoRetry: sending tx", types.Messages, "tx_id", id)
	resp, _, _, broadcastErr := m.broadcastMessage(id, rawTx)
	return resp, broadcastErr
}

func (m *manager) SendTransactionSyncNoRetry(msg proto.Message) (*ctypes.ResultTx, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionSyncNoRetry: sending tx", types.Messages, "tx_id", id)
	resp, _, _, err := m.broadcastMessage(id, msg)
	if err != nil {
		return nil, err
	}

	logging.Debug("Transaction broadcast successful (or pending if async)", types.Messages, "id", id, "txHash", resp.TxHash)
	result, err := m.WaitForResponse(resp.TxHash)
	if err != nil {
		logging.Error("Failed to wait for transaction", types.Messages, "error", err)
		return nil, err
	}
	return result, nil
}

func (m *manager) SignBytes(seed []byte) ([]byte, error) {
	name := m.account.Name
	// Kind of guessing here, not sure if this is the right way to sign bytes, will need to test
	bytes, _, err := m.client.Context().Keyring.Sign(name, seed, signing.SignMode_SIGN_MODE_DIRECT)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

func (m *manager) putOnRetry(
	id,
	txHash string,
	sequence,
	timeoutBlock uint64,
	rawTx sdk.Msg,
	sent bool) error {
	logging.Debug("putOnRetry: tx with params", types.Messages,
		"tx_id", id,
		"tx_hash", txHash,
		"sequence", sequence,
		"block_timeout", timeoutBlock,
		"sent", sent,
	)

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
			TimeOutHeight: timeoutBlock,
			Sequence:      sequence,
		}, Sent: sent})
	if err != nil {
		return err
	}
	_, err = m.js.Publish(server.TxsToSendStream, b)
	return err
}

func (m *manager) putTxToObserve(id string, rawTx sdk.Msg, txHash string, sequence, timeOutHeight uint64) error {
	logging.Debug(" putTxToObserve: tx with params", types.Messages,
		"tx_id", id,
		"tx_hash", txHash,
		"sequence", sequence,
	)

	bz, err := m.client.Context().Codec.MarshalInterfaceJSON(rawTx)
	if err != nil {
		return err
	}

	b, err := json.Marshal(&txInfo{
		Id:            id,
		RawTx:         bz,
		TxHash:        txHash,
		Sequence:      sequence,
		TimeOutHeight: timeOutHeight,
	})
	if err != nil {
		return err
	}
	_, err = m.js.Publish(server.TxsToObserveStream, b)
	return err
}

func (m *manager) SendTxs() error {
	logging.Info("SendTxs: run in background", types.Messages)

	_, err := m.js.Subscribe(server.TxsToSendStream, func(msg *nats.Msg) {
		if m.isRetryTxsPaused() {
			logging.Info("SendTxs: sending txs is paused", types.Messages)
			msg.NakWithDelay(defaultSenderNackDelay)
			return
		}

		var tx txToSend
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			logging.Error("error unmarshaling tx_to_send", types.Messages, "err", err)
			msg.Term() // malformed, drop it
			return
		}

		logging.Debug("SendTxs: got tx", types.Messages, "id", tx.TxInfo.Id)

		rawTx, err := m.unpackTx(tx.TxInfo.RawTx)
		if err != nil {
			logging.Error("error unpacking raw tx", types.Messages, "id", tx.TxInfo.Id, "err", err)
			msg.Term() // malformed, drop it
			return
		}

		if !tx.Sent {
			logging.Debug("start broadcast tx async", types.Messages, "id", tx.TxInfo.Id)
			resp, sequence, blockTimeout, err := m.broadcastMessage(tx.TxInfo.Id, rawTx)
			if err != nil {
				if isTxErrorCritical(err) {
					logging.Error("got critical error sending tx", types.Messages, "id", tx.TxInfo.Id)
					msg.Term() // invalid tx, drop it
					return
				}

				if isAccountSequenceMismatchError(err) {
					logging.Error("error sending tx", types.Messages, "id", tx.TxInfo.Id)
					if err := m.putTxToObserve(tx.TxInfo.Id, rawTx, tx.TxInfo.TxHash, tx.TxInfo.Sequence, 0); err != nil {
						logging.Error("error pushing to observe queue", types.Messages, "id", tx.TxInfo.Id, "err", err)
						msg.NakWithDelay(defaultSenderNackDelay)
					} else {
						msg.Ack()
					}
				}

				msg.NakWithDelay(defaultSenderNackDelay)
				return
			}

			tx.TxInfo.TimeOutHeight = blockTimeout
			tx.TxInfo.TxHash = resp.TxHash
			tx.TxInfo.Sequence = sequence
			tx.Sent = true
		}

		logging.Debug("tx broadcasted, put to observe", types.Messages, "id", tx.TxInfo.Id, "tx_hash", tx.TxInfo.TxHash, "block_timeout", tx.TxInfo.TimeOutHeight)

		if err := m.putTxToObserve(tx.TxInfo.Id, rawTx, tx.TxInfo.TxHash, tx.TxInfo.Sequence, tx.TxInfo.TimeOutHeight); err != nil {
			logging.Error("error pushing to observe queue", types.Messages, "id", tx.TxInfo.Id, "err", err)
			msg.NakWithDelay(defaultSenderNackDelay)
		} else {
			msg.Ack()
		}
	}, nats.Durable(txSenderConsumer), nats.ManualAck())
	return err
}

func (m *manager) ObserveTxs() error {
	logging.Info("ObserveTxs: starting in background", types.Messages)

	_, err := m.js.AddConsumer(server.TxsToObserveStream, &nats.ConsumerConfig{
		Durable:       txObserverConsumer,
		DeliverPolicy: nats.DeliverAllPolicy,
		AckPolicy:     nats.AckExplicitPolicy,
	})
	if err != nil && !strings.Contains(err.Error(), "exists") {
		return err
	}

	logging.Info("ObserveTxs: consumer created", types.Messages)

	sub, err := m.js.PullSubscribe(
		"",
		txObserverConsumer,
		nats.Bind(server.TxsToObserveStream, txObserverConsumer),
	)
	if err != nil {
		return err
	}

	var earliestFailedTxSequence uint64
	go func() {
		for {
			msgs, err := sub.Fetch(100, nats.MaxWait(3*time.Second))
			if err != nil {
				if errors.Is(err, nats.ErrTimeout) {
					// no messages in the queue
					if m.isRetryTxsPaused() {
						if err := m.setUpSequenceFromBlockchain(); err != nil {
							logging.Error("Failed to setup new sequence", types.Messages, "error", err)
							continue
						}

						earliestFailedTxSequence = 0
						m.resumeSendTxs()
						continue
					}
				}
				continue
			}

			for _, msg := range msgs {
				var tx txInfo
				if err := json.Unmarshal(msg.Data, &tx); err != nil {
					logging.Error("ObserveTxs: error unmarshaling tx_to_observe", types.Messages, "err", err)
					msg.Term() // drop malformed
					continue
				}

				logging.Debug("ObserveTxs: check tx", types.Messages, "txHash", tx.TxHash, "tx_id", tx.Id)

				rawTx, err := m.unpackTx(tx.RawTx)
				if err != nil {
					msg.Term() // drop malformed
					continue
				}

				if tx.TxHash == "" {
					logging.Warn("tx hash is empty", types.Messages, "tx_id", tx.Id)
					if err := m.putOnRetry(tx.Id, "", 0, 0, rawTx, false); err != nil {
						msg.NakWithDelay(defaultObserverNackDelay)
						continue
					}
					msg.Ack()
					continue
				}

				if m.isRetryTxsPaused() && tx.Sequence > earliestFailedTxSequence {
					if err := m.putOnRetry(tx.Id, "", 0, 0, rawTx, false); err != nil {
						msg.NakWithDelay(defaultObserverNackDelay)
						continue
					}
					msg.Ack()
					continue
				}

				found, err := m.checkTxStatus(tx.TxHash)
				if found {
					logging.Debug("tx found, remove tx from observer queue", types.Messages, "txHash", tx.TxHash, "tx_id", tx.Id)
					if err := msg.Ack(); err != nil {
						logging.Error("ack error", types.Messages, "tx_id", tx.Id, "err", err)
					}
					continue
				}

				if errors.Is(err, ErrDecodingTxHash) {
					msg.Ack() // malformed, drop
					continue
				}

				if !errors.Is(err, ErrTxNotFound) {
					msg.NakWithDelay(defaultObserverNackDelay)
					continue
				}

				currentHeight, err := m.client.LatestBlockHeight(m.ctx)
				if err != nil {
					logging.Error("error getting latest block", types.Messages, "err", err)
					msg.NakWithDelay(defaultObserverNackDelay)
					continue
				}

				logging.Debug("check block_timeout", types.Messages, "tx_id", tx.Id, "timeout", tx.TimeOutHeight, "current block", currentHeight)
				if uint64(currentHeight) < tx.TimeOutHeight {
					// tx is not expired
					if m.isRetryTxsPaused() {
						logging.Debug("sending paused: tx is not expired, put 'on retry' to clean up queue ", types.Messages, "txHash", tx.TxHash, "tx_id", tx.Id)
						if err := m.putOnRetry(tx.Id, tx.TxHash, tx.Sequence, tx.TimeOutHeight, rawTx, true); err != nil {
							msg.NakWithDelay(defaultObserverNackDelay)
							continue
						}
						msg.Ack()
						continue
					}
					logging.Debug("sending NOT paused: tx is NOT expired, do nothing", types.Messages, "txHash", tx.TxHash, "tx_id", tx.Id)
					msg.NakWithDelay(defaultObserverNackDelay)
					continue
				} else {
					if !m.isRetryTxsPaused() {
						m.pauseSendTxs()
						earliestFailedTxSequence = tx.Sequence
					}

					if tx.Sequence < earliestFailedTxSequence {
						earliestFailedTxSequence = tx.Sequence
					}

					if err := m.putOnRetry(tx.Id, "", 0, 0, rawTx, false); err != nil {
						msg.NakWithDelay(defaultObserverNackDelay)
						continue
					}
					msg.Ack()
				}
			}
		}
	}()
	return nil
}

func (m *manager) GetClientContext() client.Context {
	return m.client.Context()
}

func (m *manager) pauseSendTxs() {
	logging.Debug("Pause sending txs", types.Messages)
	m.paused = true
}

func (m *manager) isRetryTxsPaused() bool {
	return m.paused
}

func (m *manager) resumeSendTxs() {
	logging.Debug("Resume sending txs", types.Messages)
	m.paused = false
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

	logging.Debug("checkTxStatus: found tx result", types.Messages, "txHash", hash, "resp", resp)
	return true, nil
}

func (m *manager) resendFailedTransactions(sub *nats.Subscription, failedTxSequence uint64, failedTxHash string) error {
	earliestKnownFailedNonce := failedTxSequence
	for {
		msgs, err := sub.Fetch(100, nats.MaxWait(1*time.Second))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break // empty queue
			}
			return err
		}

		for _, msg := range msgs {
			var tx txInfo
			if err := json.Unmarshal(msg.Data, &tx); err != nil {
				msg.Ack()
				continue
			}

			rawTx, err := m.unpackTx(tx.RawTx)
			if err != nil {
				msg.Ack()
				continue
			}

			if tx.TxHash == failedTxHash {
				if err := m.putOnRetry(tx.Id, "", 0, tx.TimeOutHeight, rawTx, false); err != nil {
					msg.NakWithDelay(defaultSenderNackDelay)
					continue
				}
				msg.Ack()
				return nil
			}

			if tx.Sequence > earliestKnownFailedNonce {
				if err := m.putOnRetry(tx.Id, "", 0, 0, rawTx, false); err != nil {
					msg.NakWithDelay(defaultSenderNackDelay)
					continue
				}
				msg.Ack()
				continue
			}

			found, err := m.checkTxStatus(tx.TxHash)
			if found {
				msg.Ack()
				continue
			}

			if errors.Is(err, ErrDecodingTxHash) {
				msg.Ack() // malformed, drop
				continue
			}

			if errors.Is(err, ErrTxNotFound) {
				earliestKnownFailedNonce = tx.Sequence
				if err := m.putOnRetry(tx.Id, "", 0, 0, rawTx, false); err != nil {
					msg.NakWithDelay(defaultSenderNackDelay)
					continue
				}
			}
			msg.NakWithDelay(defaultSenderNackDelay)
			continue
		}
	}
	return nil
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

func ParseMsgResponse(data []byte, msgIndex int, dstMsg proto.Message) error {
	var txMsgData sdk.TxMsgData
	if err := proto.Unmarshal(data, &txMsgData); err != nil {
		logging.Error("Failed to unmarshal TxMsgData", types.Messages, "error", err, "data", data)
		return fmt.Errorf("failed to unmarshal TxMsgData: %w", err)
	}

	logging.Info("Found messages", types.Messages, "len(messages)", len(txMsgData.MsgResponses), "messages", txMsgData.MsgResponses)
	if msgIndex < 0 || msgIndex >= len(txMsgData.MsgResponses) {
		logging.Error("Message index out of range", types.Messages, "msgIndex", msgIndex, "len(messages)", len(txMsgData.MsgResponses))
		return fmt.Errorf(
			"message index %d out of range: got %d responses",
			msgIndex, len(txMsgData.MsgResponses),
		)
	}

	anyResp := txMsgData.MsgResponses[msgIndex]
	if err := proto.Unmarshal(anyResp.Value, dstMsg); err != nil {
		logging.Error("Failed to unmarshal response", types.Messages, "error", err, "msgIndex", msgIndex, "response", anyResp.Value)
		return fmt.Errorf("failed to unmarshal response at index %d: %w", msgIndex, err)
	}
	return nil
}

func (m *manager) broadcastMessage(id string, rawTx sdk.Msg) (*sdk.TxResponse, uint64, uint64, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	address, err := m.account.Record.GetAddress()
	if err != nil {
		logging.Error("Failed to get account address", types.Messages, "tx_id", id, "error", err)
		return nil, 0, 0, err
	}

	accountNumber, sequence, err := m.accountRetriever.GetAccountNumberSequence(m.client.Context(), address)
	if err != nil {
		logging.Error("Failed to get account number and sequence", types.Messages, "tx_id", id, "error", err)
		return nil, 0, 0, err
	}

	logging.Debug("Got account number and sequence", types.Messages, "tx_id", id, "blockchain_sequence", sequence, "highest_sequence", m.highestSequence)

	if int64(sequence) <= m.highestSequence {
		logging.Info("Factory sequence is lower or equal than highest sequence", types.Messages, "blockchain_sequence", sequence, "highestSequence", m.highestSequence)
		sequence = uint64(m.highestSequence) + 1
	}

	currentHeight, err := m.client.LatestBlockHeight(m.ctx)
	if err != nil {
		logging.Error("Failed to latest block", types.Messages, "tx_id", id, "error", err)
		return nil, 0, 0, err
	}

	timeout := uint64(currentHeight) + m.blockTimeout
	logging.Info(
		"broadcast message: tx params", types.Messages,
		"tx_id", id,
		"sequence", sequence,
		"account_name", m.account.Name,
		"accountNumber", accountNumber,
		"block_timeout", timeout)

	factory := m.client.TxFactory.
		WithSequence(sequence).
		WithAccountNumber(accountNumber).
		WithGasAdjustment(10).
		WithFees("").
		WithGasPrices("").
		WithGas(0).
		WithTimeoutHeight(timeout)

	unsignedTx, err := factory.BuildUnsignedTx(rawTx)
	if err != nil {
		logging.Error("error building unsigned tx", types.Messages, "tx_id", id, "error", err)
		return nil, 0, 0, ErrBuildingUnsignedTx
	}

	unsignedTx.SetGasLimit(1000000000)
	unsignedTx.SetFeeAmount(sdk.Coins{})

	err = txclient.Sign(m.ctx, factory, m.account.Name, unsignedTx, false)
	if err != nil {
		logging.Error("Failed to sign transaction", types.Messages, "tx_id", id, "error", err)
		return nil, 0, 0, ErrFailedToSignTx
	}

	txBytes, err := m.client.Context().TxConfig.TxEncoder()(unsignedTx.GetTx())
	if err != nil {
		logging.Error("Failed to encode transaction", types.Messages, "tx_id", id, "error", err)
		return nil, 0, 0, ErrFailedToEncodeTx
	}

	resp, err := m.client.Context().BroadcastTxSync(txBytes)
	if err != nil {
		logging.Error("broadcast message: failed to broadcast, try later", types.Messages, "id", id, "err", err)
		return nil, 0, 0, err
	}

	if resp.Code > 0 {
		err = NewTransactionErrorFromResponse(resp)
		logging.Error("broadcast message: transaction failed during CheckTx or DeliverTx (sync/block mode)", types.Messages, "id", id, "err", err)
		return nil, 0, 0, err
	}

	m.highestSequence = int64(factory.Sequence())
	return resp, sequence, unsignedTx.GetTx().GetTimeoutHeight(), nil
}

func (m *manager) setUpSequenceFromBlockchain() error {
	address, err := m.account.Record.GetAddress()
	if err != nil {
		return err
	}
	_, sequence, err := m.accountRetriever.GetAccountNumberSequence(m.client.Context(), address)
	if err != nil {
		return err
	}

	logging.Debug("setUpSequenceFromBlockchain: setup sequence", types.Messages, "blockchain_sequence", sequence, "current highestSequence", m.highestSequence)
	m.mtx.Lock()
	defer m.mtx.Unlock()
	if m.highestSequence != int64(sequence) {
		m.highestSequence = int64(sequence) - 1
	}
	return nil
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
