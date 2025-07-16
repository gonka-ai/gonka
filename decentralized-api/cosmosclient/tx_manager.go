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
	"time"

	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/x/inference/types"
	"strings"
	"sync/atomic"
)

const (
	txSenderConsumer   = "tx-sender"
	txObserverConsumer = "tx-observer"

	defaultBlockTimeout = uint64(300) // around 30 mins if block is produced every 5-6 sec
	defaultNackDelay    = time.Second * 30
)

type TxManager interface {
	GetClientContext() client.Context
	SignBytes(seed []byte) ([]byte, error)
	PutTxToSend(rawTx sdk.Msg) error
	SendTransactionBlocking(msg proto.Message) (*ctypes.ResultTx, error)
	BankBalances(ctx context.Context, address string) ([]sdk.Coin, error)
	SendTxs() error
	ObserveTxs() error
}

type manager struct {
	client           *cosmosclient.Client
	account          *cosmosaccount.Account
	accountRetriever client.AccountRetriever
	address          string
	highestSequence  atomic.Int64
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
	address string, blockTimeout uint64) (TxManager, error) {
	startSeq := atomic.Int64{}
	startSeq.Store(-1)
	js, err := nc.JetStream()
	if err != nil {
		return nil, err
	}

	types.RegisterInterfaces(client.Context().InterfaceRegistry)

	return &manager{
		ctx:              ctx,
		client:           client,
		highestSequence:  startSeq,
		address:          address,
		account:          account,
		accountRetriever: accountRetriever,
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
	TimeOutHeight uint64
}

func (m *manager) PutTxToSend(rawTx sdk.Msg) error {
	bz, err := m.client.Context().Codec.MarshalInterfaceJSON(rawTx)
	if err != nil {
		return err
	}

	b, err := json.Marshal(&txToSend{TxInfo: txInfo{Id: uuid.New().String(), RawTx: bz}})
	if err != nil {
		return err
	}
	_, err = m.js.Publish(server.TxsToSendStream, b)
	return err
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

func (m *manager) putTxToObserve(rawTx sdk.Msg, txHash string, timeOutHeight uint64) error {
	bz, err := m.client.Context().Codec.MarshalInterfaceJSON(rawTx)
	if err != nil {
		return err
	}

	b, err := json.Marshal(&txInfo{
		RawTx:         bz,
		TxHash:        txHash,
		TimeOutHeight: timeOutHeight,
	})
	if err != nil {
		return err
	}
	_, err = m.js.Publish(server.TxsToObserveStream, b)
	return err
}

func (m *manager) SendTxs() error {
	_, err := m.js.Subscribe(server.TxsToSendStream, func(msg *nats.Msg) {
		if m.paused {
			logging.Info("sending txs is paused", types.Messages)
			return
		}

		var tx txToSend
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			logging.Error("error unmarshaling tx_to_send", types.Messages, "err", err)
			msg.Ack() // malformed, drop it
			return
		}

		rawTx, err := m.unpackTx(tx.TxInfo.RawTx)
		if err != nil {
			logging.Error("error unpacking raw tx", types.Messages, "id", tx.TxInfo.Id, "err", err)
			msg.Ack() // malformed, drop it
			return
		}

		if !tx.Sent {
			logging.Debug("Start Broadcast", types.Messages, "id", tx.TxInfo.Id)
			txBytes, blockTimeout, err := m.buildAndSignTx(rawTx)
			if err != nil {
				msg.NakWithDelay(defaultNackDelay)
				return
			}

			resp, err := m.client.Context().BroadcastTxSync(txBytes)
			if err != nil || resp.Code > 0 {
				msg.NakWithDelay(defaultNackDelay)
				return
			}

			m.highestSequence.Add(1)
			tx.TxInfo.TimeOutHeight = blockTimeout
			tx.TxInfo.TxHash = resp.TxHash
			tx.Sent = true
		}

		if err := m.putTxToObserve(rawTx, tx.TxInfo.TxHash, tx.TxInfo.TimeOutHeight); err != nil {
			msg.NakWithDelay(defaultNackDelay)
		} else {
			msg.Ack()
		}
	}, nats.Durable(txSenderConsumer), nats.ManualAck())
	return err
}

func (m *manager) ObserveTxs() error {
	_, err := m.js.Subscribe(server.TxsToObserveStream, func(msg *nats.Msg) {
		var tx txInfo
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			msg.Ack() // drop malformed
			return
		}

		bz, err := hex.DecodeString(tx.TxHash)
		if err != nil {
			msg.Ack()
			return
		}

		_, err = m.client.Context().Client.Tx(m.ctx, bz, false)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				currentHeight, err := m.client.LatestBlockHeight(m.ctx)
				if err != nil {
					return // retry later
				}
				if uint64(currentHeight) > tx.TimeOutHeight {
					logging.Debug("Transaction wasn't included in block within timeout: try to resend", types.Messages, "tx_hash", tx.TxHash, "tx_timeout_block", tx.TimeOutHeight, "current_height", currentHeight, "id", tx.Id)
					m.pauseSendTxs()

					logging.Debug("Pause sending txs", types.Messages, "id", tx.TxHash, "tx_timeout_block", tx.TimeOutHeight)

					if err := m.resendAllTxs(); err != nil {
						logging.Error("Failed to resend transactions batch", types.Messages, "err", err)
					}

					if err := m.setUpSequenceFromBlockchain(); err != nil {
						logging.Error("Failed to setup new sequence", types.Messages, "error", err)
					}
					m.resumeSendTxs()
					logging.Debug("Resume sending txs", types.Messages, "id", tx.TxHash, "tx_timeout_block", tx.TimeOutHeight)
				}
			}
			return
		}
		msg.Ack()
	}, nats.Durable(txObserverConsumer), nats.ManualAck())
	return err
}

func (m *manager) GetClientContext() client.Context {
	return m.client.Context()
}

func (m *manager) pauseSendTxs() {
	m.paused = true
}

func (m *manager) resumeSendTxs() {
	m.paused = false
}

func (m *manager) resendAllTxs() error {
	sub, err := m.js.PullSubscribe(server.TxsToObserveStream, txObserverConsumer, nats.Bind(server.TxsToSendStream, txObserverConsumer))
	if err != nil {
		return err
	}

	for {
		msgs, err := sub.Fetch(100, nats.MaxWait(2*time.Second))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			} else {
				return err
			}
		}

		for _, msg := range msgs {
			var tx txInfo
			if err := json.Unmarshal(msg.Data, &tx); err != nil {
				msg.Ack() // drop malformed
				continue
			}

			rawTx, err := m.unpackTx(tx.RawTx)
			if err != nil {
				msg.Ack() // drop malformed
				continue
			}

			if err := m.PutTxToSend(rawTx); err != nil {
				msg.NakWithDelay(defaultNackDelay)
				continue
			}
			msg.Ack()
		}
	}
	return nil
}

func (m *manager) SendTransactionBlocking(msg proto.Message) (*ctypes.ResultTx, error) {
	id := uuid.New().String()
	signedTx, _, err := m.buildAndSignTx(msg)
	if err != nil {
		return nil, err
	}

	response, err := m.client.Context().BroadcastTxSync(signedTx)
	if err != nil {
		return nil, err
	}

	if response.Code != 0 {
		logging.Error("Transaction failed during CheckTx or DeliverTx (sync/block mode)", types.Messages, "id", id, "response", response)
		return nil, NewTransactionErrorFromResponse(response)
	}

	logging.Debug("Transaction broadcast successful (or pending if async)", types.Messages, "id", id, "txHash", response.TxHash)
	result, err := m.WaitForResponse(response.TxHash)
	if err != nil {
		logging.Error("Failed to wait for transaction", types.Messages, "error", err)
		return nil, err
	}
	return result, nil
}

func (m *manager) WaitForResponse(txHash string) (*ctypes.ResultTx, error) {
	transactionAppliedResult, err := m.client.WaitForTx(m.ctx, txHash)
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

func (m *manager) buildAndSignTx(rawTx sdk.Msg) ([]byte, uint64, error) {
	address, err := m.account.Record.GetAddress()
	if err != nil {
		logging.Error("Failed to get account address", types.Messages, "error", err)
		return nil, 0, err
	}

	accountNumber, sequence, err := m.accountRetriever.GetAccountNumberSequence(m.client.Context(), address)
	if err != nil {
		logging.Error("Failed to get account number and sequence", types.Messages, "error", err)
		return nil, 0, err
	}

	if int64(sequence) <= m.highestSequence.Load() {
		logging.Info("Factory sequence is lower or equal than highest sequence", types.Messages, "sequence", sequence, "highestSequence", m.highestSequence.Load())
		sequence = uint64(m.highestSequence.Load())
	}

	currentHeight, err := m.client.LatestBlockHeight(m.ctx)
	if err != nil {
		logging.Error("Failed to latest block", types.Messages, "error", err)
		return nil, 0, err
	}

	timeout := uint64(currentHeight) + m.blockTimeout
	logging.Info(
		"Build tx params", types.Messages,
		"sequence", sequence,
		"account_name", m.account.Name,
		"accountNumber", accountNumber,
		"block_timeout", timeout,
		"chain_id", m.client.Context().ChainID)

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
		return nil, 0, err
	}

	err = txclient.Sign(m.ctx, factory, m.account.Name, unsignedTx, false)
	if err != nil {
		logging.Error("Failed to sign transaction", types.Messages, "error", err)
		return nil, 0, err
	}

	txBytes, err := m.client.Context().TxConfig.TxEncoder()(unsignedTx.GetTx())
	if err != nil {
		logging.Error("Failed to encode transaction", types.Messages, "error", err)
		return nil, 0, err
	}

	return txBytes, unsignedTx.GetTx().GetTimeoutHeight(), nil
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

	if m.highestSequence.Load() > int64(sequence) {
		m.highestSequence.Store(int64(sequence) + 1)
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
