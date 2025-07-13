package cosmosclient

import (
	"context"
	"decentralized-api/logging"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cosmos/cosmos-sdk/client"
	txclient "github.com/cosmos/cosmos-sdk/client/tx"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/x/inference/types"
	"strings"
	"sync/atomic"
)

const (
	TxsToSendTopic    = "txs_to_send"
	TxsToObserveTopic = "txs_to_observe"

	txObserverConsumer = "tx-observer"
	txSenderConsumer   = "tx-sender"

	defaultBlockTimeout = uint64(300) // around 30 mins if block is produced every 5-6 sec
	defaultNackDelay    = time.Second * 30
)

type TxManager interface {
	PutTxToSend(rawTx sdk.Msg) error
	SendTxs() error
	ObserveTxs() error
}

type manager struct {
	client          *cosmosclient.Client
	account         *cosmosaccount.Account
	address         string
	currentSequence atomic.Int64

	nc     *nats.Conn
	js     nats.JetStreamContext
	ctx    context.Context
	paused bool
}

func NewTxManager(client *cosmosclient.Client, account *cosmosaccount.Account, address string) TxManager {
	startSeq := atomic.Int64{}
	startSeq.Store(-1)
	return &manager{
		client:          client,
		currentSequence: startSeq,
		address:         address,
		account:         account,
	}
}

type txToSend struct {
	txInfo txInfo
	sent   bool
}

type txInfo struct {
	rawTx         sdk.Msg
	txHash        string
	timeOutHeight uint64
}

func (m *manager) PutTxToSend(rawTx sdk.Msg) error {
	b, err := json.Marshal(&txToSend{txInfo: txInfo{rawTx: rawTx}})
	if err != nil {
		return err
	}
	_, err = m.js.Publish(TxsToSendTopic, b)
	return err
}

func (m *manager) putTxToObserve(rawTx sdk.Msg, txHash string, timeOutHeight uint64) error {
	b, err := json.Marshal(&txInfo{
		rawTx:         rawTx,
		txHash:        txHash,
		timeOutHeight: timeOutHeight,
	})
	if err != nil {
		return err
	}
	_, err = m.js.Publish(TxsToObserveTopic, b)
	return err
}

func (m *manager) SendTxs() error {
	_, err := m.js.Subscribe(TxsToSendTopic, func(msg *nats.Msg) {
		if m.paused {
			logging.Info("sending txs is paused", types.Messages)
			return
		}

		var tx txToSend
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			msg.Ack() // malformed, drop it
			return
		}

		if !tx.sent {
			factory, err := m.getFactory()
			if err != nil {
				msg.NakWithDelay(defaultNackDelay)
				return
			}

			unsignedTx, err := factory.BuildUnsignedTx(tx.txInfo.rawTx)
			if err != nil {
				msg.NakWithDelay(defaultNackDelay)
				return
			}

			txBytes, err := m.getSignedBytes(m.ctx, unsignedTx, factory)
			if err != nil {
				msg.NakWithDelay(defaultNackDelay)
				return
			}

			resp, err := m.client.Context().BroadcastTxSync(txBytes)
			if err != nil || resp.Code > 0 {
				msg.NakWithDelay(defaultNackDelay)
				return
			}

			m.currentSequence.Add(1)
			tx.txInfo.timeOutHeight = factory.TimeoutHeight()
			tx.txInfo.txHash = resp.TxHash
			tx.sent = true
		}

		if err := m.putTxToObserve(tx.txInfo.rawTx, tx.txInfo.txHash, tx.txInfo.timeOutHeight); err != nil {
			msg.NakWithDelay(defaultNackDelay)
		} else {
			msg.Ack()
		}
	}, nats.Durable("tx-manager"), nats.ManualAck())
	return err
}

func (m *manager) ObserveTxs() error {
	_, err := m.js.Subscribe(TxsToObserveTopic, func(msg *nats.Msg) {
		var tx txInfo
		if err := json.Unmarshal(msg.Data, &tx); err != nil {
			msg.Ack() // drop malformed
			return
		}

		bz, err := hex.DecodeString(tx.txHash)
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
				if uint64(currentHeight) > tx.timeOutHeight {
					m.pauseSendTxs()
					logging.Info("Transaction wasn't included in block within timeout: try to resend", types.Messages, "tx_hash", tx.txHash, "tx_timeout_block", tx.timeOutHeight, "current_height", currentHeight)

					if err := m.resendAllTxs(); err != nil {
						logging.Error("Failed to resend transactions batch", types.Messages, "err", err)
					}

					if err := m.setUpSequenceFromBlockchain(); err != nil {
						logging.Error("Failed to setup new sequence", types.Messages, "error", err)
					}
					m.resumeSendTxs()
				}
			}
			return
		}
		msg.Ack()
	}, nats.Durable("tx-observer"), nats.ManualAck())
	return err
}

func (m *manager) pauseSendTxs() {
	m.paused = true
}

func (m *manager) resumeSendTxs() {
	m.paused = false
}

func (m *manager) resendAllTxs() error {
	sub, err := m.js.PullSubscribe("", txObserverConsumer, nats.Bind(TxsToObserveTopic, txObserverConsumer))
	if err != nil {
		return err
	}

	for {
		msgs, err := sub.Fetch(100, nats.MaxWait(2*time.Second))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			} else {
				return fmt.Errorf("fetch error: %w", err)
			}
		}

		for _, msg := range msgs {
			var tx txInfo
			if err := json.Unmarshal(msg.Data, &tx); err != nil {
				msg.Ack() // drop malformed
			}
			if err := m.PutTxToSend(tx.rawTx); err != nil {
				msg.Nak()
				continue
			}
			msg.Ack()
		}
	}
	return nil
}

func (m *manager) getFactory() (*txclient.Factory, error) {
	address, err := m.account.Record.GetAddress()
	if err != nil {
		logging.Error("Failed to get account address", types.Messages, "error", err)
		return nil, err
	}
	accountNumber, sequence, err := accountRetriever.GetAccountNumberSequence(m.client.Context(), address)
	if err != nil {
		logging.Error("Failed to get account number and sequence", types.Messages, "error", err)
		return nil, err
	}
	if int64(sequence) <= m.currentSequence.Load() {
		logging.Info("Factory sequence is lower or equal than highest sequence", types.Messages, "sequence", sequence, "highestSequence", highestSequence)
		sequence = uint64(m.currentSequence.Load() + 1)
	}
	logging.Debug("Transaction sequence", types.Messages, "sequence", sequence, "accountNumber", accountNumber)
	factory := m.client.TxFactory.
		WithSequence(sequence).
		WithAccountNumber(accountNumber).
		WithGasAdjustment(10).
		WithFees("").
		WithGasPrices("").
		WithGas(0).
		WithTimeoutHeight(uint64(m.client.Context().Height) + defaultBlockTimeout) // TODO check if cur block height is correct
	return &factory, nil
}

func (m *manager) getSignedBytes(ctx context.Context, unsignedTx client.TxBuilder, factory *txclient.Factory) ([]byte, error) {
	unsignedTx.SetGasLimit(1000000000)
	unsignedTx.SetFeeAmount(sdk.Coins{})
	name := m.account.Name
	logging.Debug("Signing transaction", types.Messages, "name", name)
	err := txclient.Sign(ctx, *factory, name, unsignedTx, false)
	if err != nil {
		logging.Error("Failed to sign transaction", types.Messages, "error", err)
		return nil, err
	}
	txBytes, err := m.client.Context().TxConfig.TxEncoder()(unsignedTx.GetTx())
	if err != nil {
		logging.Error("Failed to encode transaction", types.Messages, "error", err)
		return nil, err
	}
	return txBytes, nil
}

func (m *manager) setUpSequenceFromBlockchain() error {
	address, err := m.account.Record.GetAddress()
	if err != nil {
		return err
	}
	_, sequence, err := accountRetriever.GetAccountNumberSequence(m.client.Context(), address)
	if err != nil {
		return err
	}

	if m.currentSequence.Load() > int64(sequence) {
		m.currentSequence.Store(int64(sequence) + 1)
	}
	return nil
}
