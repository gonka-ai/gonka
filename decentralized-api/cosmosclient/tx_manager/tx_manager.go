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
	"time"

	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/x/inference/types"
	"strings"
)

const (
	txSenderConsumer   = "tx-sender"
	txObserverConsumer = "tx-observer"

	defaultSenderNackDelay   = time.Second * 7
	defaultObserverNackDelay = time.Second * 5
)

type TxManager interface {
	SendTransactionAsyncWithRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error)
	SendTransactionSyncNoRetry(msg proto.Message) (*ctypes.ResultTx, error)
	GetClientContext() client.Context
	GetKeyring() *keyring.Keyring
	GetApiAccount() apiconfig.ApiAccount
	Status(ctx context.Context) (*ctypes.ResultStatus, error)
	BankBalances(ctx context.Context, address string) ([]sdk.Coin, error)
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
}

func StartTxManager(
	ctx context.Context,
	client *cosmosclient.Client,
	account *apiconfig.ApiAccount,
	defaultTimeout time.Duration,
	natsConnection *nats.Conn,
	address string) (*manager, error) {
	js, err := natsConnection.JetStream()
	if err != nil {
		return nil, err
	}

	types.RegisterInterfaces(client.Context().InterfaceRegistry)

	m := &manager{
		ctx:              ctx,
		client:           client,
		address:          address,
		apiAccount:       account,
		accountRetriever: authtypes.AccountRetriever{},
		defaultTimeout:   defaultTimeout,
		natsConnection:   natsConnection,
		natsJetStream:    js,
	}

	if err := m.sendTxs(); err != nil {
		return nil, err
	}

	if err := m.observeTxs(); err != nil {
		return nil, err
	}

	return &manager{
		ctx:              ctx,
		client:           client,
		address:          address,
		apiAccount:       account,
		accountRetriever: authtypes.AccountRetriever{},
		defaultTimeout:   defaultTimeout,
		natsConnection:   natsConnection,
		natsJetStream:    js,
	}, nil
}

type txToSend struct {
	TxInfo txInfo
	Sent   bool
}

type txInfo struct {
	Id      string
	RawTx   []byte
	TxHash  string
	Timeout time.Time
}

func (m *manager) GetApiAccount() apiconfig.ApiAccount {
	return *m.apiAccount
}

func (m *manager) Status(ctx context.Context) (*ctypes.ResultStatus, error) {
	return m.client.Status(ctx)
}

func (m *manager) SendTransactionAsyncWithRetry(rawTx sdk.Msg) (*sdk.TxResponse, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionAsyncWithRetry: sending tx", types.Messages, "tx_id", id)
	resp, timeout, broadcastErr := m.broadcastMessage(id, rawTx)
	if broadcastErr != nil {
		if isTxErrorCritical(broadcastErr) {
			logging.Error("SendTransactionAsyncWithRetry: critical error sending tx", types.Messages, "tx_id", id, "err", broadcastErr)
			return nil, broadcastErr
		}

		err := m.putOnRetry(id, "", timeout, rawTx, false)
		if err != nil {
			logging.Error("tx failed to broadcast, failed to put in queue", types.Messages, "tx_id", id, "broadcast_err", broadcastErr, "resend_err", err)
		}
		return nil, ErrTxFailedToBroadcastAndPutOnRetry
	}
	if err := m.putOnRetry(id, resp.TxHash, timeout, rawTx, true); err != nil {
		logging.Error("tx broadcast, but failed to put in queue", types.Messages, "tx_id", id, "err", err)
	}
	return resp, nil
}

func (m *manager) SendTransactionAsyncNoRetry(rawTx sdk.Msg) (*sdk.TxResponse, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionAsyncNoRetry: sending tx", types.Messages, "tx_id", id)
	resp, _, broadcastErr := m.broadcastMessage(id, rawTx)
	return resp, broadcastErr
}

func (m *manager) SendTransactionSyncNoRetry(msg proto.Message) (*ctypes.ResultTx, error) {
	id := uuid.New().String()
	logging.Debug("SendTransactionSyncNoRetry: sending tx", types.Messages, "tx_id", id)
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
	sent bool) error {
	logging.Debug("putOnRetry: tx with params", types.Messages,
		"tx_id", id,
		"tx_hash", txHash,
		"timeout", timeout.String(),
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
			Id:      id,
			RawTx:   bz,
			TxHash:  txHash,
			Timeout: timeout,
		}, Sent: sent})
	if err != nil {
		return err
	}
	_, err = m.natsJetStream.Publish(server.TxsToSendStream, b)
	return err
}

func (m *manager) putTxToObserve(id string, rawTx sdk.Msg, txHash string, timeout time.Time) error {
	logging.Debug(" putTxToObserve: tx with params", types.Messages,
		"tx_id", id,
		"tx_hash", txHash,
		"timeout", timeout.String(),
	)

	bz, err := m.client.Context().Codec.MarshalInterfaceJSON(rawTx)
	if err != nil {
		return err
	}

	b, err := json.Marshal(&txInfo{
		Id:      id,
		RawTx:   bz,
		TxHash:  txHash,
		Timeout: timeout,
	})
	if err != nil {
		return err
	}
	_, err = m.natsJetStream.Publish(server.TxsToObserveStream, b)
	return err
}

func (m *manager) sendTxs() error {
	logging.Info("Tx manager: sending txs: run in background", types.Messages)

	_, err := m.natsJetStream.Subscribe(server.TxsToSendStream, func(msg *nats.Msg) {
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
			resp, timeout, err := m.broadcastMessage(tx.TxInfo.Id, rawTx)
			if err != nil {
				if isTxErrorCritical(err) {
					logging.Error("got critical error sending tx", types.Messages, "id", tx.TxInfo.Id)
					msg.Term() // invalid tx, drop it
					return
				}
				msg.NakWithDelay(defaultSenderNackDelay)
				return
			}
			tx.TxInfo.Timeout = timeout
			tx.TxInfo.TxHash = resp.TxHash
			tx.Sent = true
		}

		logging.Debug("tx broadcast, put to observe", types.Messages, "id", tx.TxInfo.Id, "tx_hash", tx.TxInfo.TxHash, "timeout", tx.TxInfo.Timeout.String())

		if err := m.putTxToObserve(tx.TxInfo.Id, rawTx, tx.TxInfo.TxHash, tx.TxInfo.Timeout); err != nil {
			logging.Error("error pushing to observe queue", types.Messages, "id", tx.TxInfo.Id, "err", err)
			msg.NakWithDelay(defaultSenderNackDelay)
		} else {
			msg.Ack()
		}
	}, nats.Durable(txSenderConsumer), nats.ManualAck())
	return err
}

func (m *manager) observeTxs() error {
	logging.Info("Tx manager: observeTxs txs: run in background", types.Messages)
	_, err := m.natsJetStream.Subscribe(server.TxsToObserveStream, func(msg *nats.Msg) {
		var tx txInfo
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			logging.Error("error unmarshaling tx_to_observe", types.Messages, "err", err)
			msg.Term()
			return
		}

		rawTx, err := m.unpackTx(tx.RawTx)
		if err != nil {
			msg.Term()
			return
		}

		if tx.TxHash == "" {
			logging.Warn("tx hash is empty", types.Messages, "tx_id", tx.Id)
			if err := m.putOnRetry(tx.Id, "", time.Time{}, rawTx, false); err != nil {
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
			if time.Now().After(tx.Timeout) {
				logging.Debug("tx expired", types.Messages, "tx_id", tx.Id, "tx_hash", tx.TxHash)
				if err := m.putOnRetry(tx.Id, "", time.Time{}, rawTx, false); err != nil {
					msg.NakWithDelay(defaultObserverNackDelay)
					return
				}
				msg.Ack()
				return
			}
		}

		msg.NakWithDelay(defaultObserverNackDelay)
		return
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

func (m *manager) broadcastMessage(id string, rawTx sdk.Msg) (*sdk.TxResponse, time.Time, error) {
	factory, err := m.getFactory(id)
	if err != nil {
		return nil, time.Time{}, err
	}

	var finalMsg sdk.Msg = rawTx
	if !m.apiAccount.IsSignerTheMainAccount() {
		granteeAddress, err := m.apiAccount.SignerAddress()
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("failed to get signer address: %w", err)
		}

		execMsg := authztypes.NewMsgExec(granteeAddress, []sdk.Msg{rawTx})
		finalMsg = &execMsg
		logging.Info("Using authz MsgExec", types.Messages, "grantee", granteeAddress.String(), "originalMsgType", sdk.MsgTypeURL(rawTx))
	}

	unsignedTx, err := factory.BuildUnsignedTx(finalMsg)
	if err != nil {
		return nil, time.Time{}, err
	}
	txBytes, timestamp, err := m.getSignedBytes(m.ctx, id, unsignedTx, factory)
	if err != nil {
		return nil, time.Time{}, err
	}

	resp, err := m.client.Context().BroadcastTxSync(txBytes)
	if err != nil {
		return nil, time.Time{}, err
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

func (m *manager) getSignedBytes(ctx context.Context, id string, unsignedTx client.TxBuilder, factory *tx.Factory) ([]byte, time.Time, error) {
	// Gas is not charged, but without a high gas limit the transactions fail
	timestamp := getTimestamp(m.defaultTimeout)

	unsignedTx.SetGasLimit(1000000000)
	unsignedTx.SetFeeAmount(sdk.Coins{})
	unsignedTx.SetUnordered(true)
	unsignedTx.SetTimeoutTimestamp(timestamp)
	name := m.apiAccount.SignerAccount.Name
	logging.Debug("Signing transaction", types.Messages, "tx_id", id, "timeout", timestamp.String(), "name", name)

	err := tx.Sign(ctx, *factory, name, unsignedTx, false)
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
