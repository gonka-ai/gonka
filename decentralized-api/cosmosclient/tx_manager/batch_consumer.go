package tx_manager

import (
	"common/logging"
	"decentralized-api/internal/nats/server"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/x/inference/types"
)

const (
	batchValidationV2Consumer = "batch-validation-v2-consumer"
	batchAckWait              = time.Minute // must exceed FlushTimeout to prevent redelivery
)

type BatchConfig struct {
	ValidationV2FlushSize    int
	ValidationV2FlushTimeout time.Duration
}

type pendingMsg struct {
	msg     sdk.Msg
	natsMsg *nats.Msg
}

type BatchConsumer struct {
	js        nats.JetStreamContext
	codec     codec.Codec
	txManager TxManager
	config    BatchConfig

	validationV2Batch []pendingMsg

	validationV2Mu sync.Mutex

	validationV2CreatedAt time.Time
}

func NewBatchConsumer(
	js nats.JetStreamContext,
	cdc codec.Codec,
	txManager TxManager,
	config BatchConfig,
) *BatchConsumer {
	return &BatchConsumer{
		js:                js,
		codec:             cdc,
		txManager:         txManager,
		config:            config,
		validationV2Batch: make([]pendingMsg, 0, config.ValidationV2FlushSize),
	}
}

func (c *BatchConsumer) Start() error {
	if err := c.subscribeStream(server.TxsBatchValidationV2Stream, batchValidationV2Consumer, c.handleValidationV2Msg); err != nil {
		return err
	}

	go c.flushLoop()
	logging.Info("Batch consumer started", types.Messages,
		"validationV2FlushSize", c.config.ValidationV2FlushSize,
		"validationV2FlushTimeout", c.config.ValidationV2FlushTimeout)
	return nil
}

func (c *BatchConsumer) subscribeStream(stream, consumer string, handler func(*nats.Msg)) error {
	_, err := c.js.Subscribe(stream, handler,
		nats.Durable(consumer),
		nats.ManualAck(),
		nats.AckWait(batchAckWait),
	)
	return err
}

func (c *BatchConsumer) handleValidationV2Msg(msg *nats.Msg) {
	if err := msg.InProgress(); err != nil {
		logging.Error("Failed to mark validation v2 msg in progress", types.Messages, "error", err)
	}
	sdkMsg, err := c.unmarshalMsg(msg.Data)
	if err != nil {
		logging.Error("Failed to unmarshal validation v2 msg", types.Messages, "error", err)
		msg.Term()
		return
	}

	var shouldFlush bool
	c.validationV2Mu.Lock()
	if len(c.validationV2Batch) == 0 {
		c.validationV2CreatedAt = time.Now()
	}
	c.validationV2Batch = append(c.validationV2Batch, pendingMsg{msg: sdkMsg, natsMsg: msg})
	shouldFlush = len(c.validationV2Batch) >= c.config.ValidationV2FlushSize
	c.validationV2Mu.Unlock()

	if shouldFlush {
		c.flushValidationV2()
	}
}

func (c *BatchConsumer) flushLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.extendAckDeadlines()
		c.checkAndFlushValidationV2()
	}
}

func (c *BatchConsumer) extendAckDeadlines() {
	c.validationV2Mu.Lock()
	for _, p := range c.validationV2Batch {
		_ = p.natsMsg.InProgress()
	}
	c.validationV2Mu.Unlock()
}

func (c *BatchConsumer) checkAndFlushValidationV2() {
	c.validationV2Mu.Lock()
	shouldFlush := len(c.validationV2Batch) > 0 && time.Since(c.validationV2CreatedAt) >= c.config.ValidationV2FlushTimeout
	c.validationV2Mu.Unlock()

	if shouldFlush {
		c.flushValidationV2()
	}
}

func (c *BatchConsumer) flushValidationV2() {
	c.validationV2Mu.Lock()
	batch := c.validationV2Batch
	if len(batch) == 0 {
		c.validationV2Mu.Unlock()
		return
	}
	c.validationV2Batch = make([]pendingMsg, 0, c.config.ValidationV2FlushSize)
	c.validationV2CreatedAt = time.Time{} // reset timer
	c.validationV2Mu.Unlock()

	// Aggregate validations by height into single messages
	aggregated := c.aggregateValidationV2Messages(batch)

	c.broadcastAggregatedValidationV2(aggregated, batch)
}

// aggregateValidationV2Messages merges multiple MsgSubmitPocValidationsV2 messages into
// single messages grouped by PocStageStartBlockHeight. This reduces chain overhead from
// N messages with 1 validation each to 1 message with N validations (per height).
func (c *BatchConsumer) aggregateValidationV2Messages(batch []pendingMsg) []sdk.Msg {
	// Group validations by height
	byHeight := make(map[int64]*types.MsgSubmitPocValidationsV2)

	for _, p := range batch {
		msg, ok := p.msg.(*types.MsgSubmitPocValidationsV2)
		if !ok {
			logging.Warn("Unexpected message type in validation V2 batch", types.Messages)
			continue
		}

		height := msg.PocStageStartBlockHeight
		existing, found := byHeight[height]
		if !found {
			// First message for this height - clone it
			byHeight[height] = &types.MsgSubmitPocValidationsV2{
				Creator:                  msg.Creator,
				PocStageStartBlockHeight: height,
				Validations:              msg.Validations,
			}
		} else {
			// Append validations to existing message
			existing.Validations = append(existing.Validations, msg.Validations...)
		}
	}

	// Convert map to slice
	result := make([]sdk.Msg, 0, len(byHeight))
	for _, msg := range byHeight {
		result = append(result, msg)
	}

	return result
}

// broadcastAggregatedValidationV2 sends aggregated validation messages and acks original NATS messages.
func (c *BatchConsumer) broadcastAggregatedValidationV2(aggregated []sdk.Msg, originalBatch []pendingMsg) {
	totalValidations := 0
	for _, msg := range aggregated {
		if v, ok := msg.(*types.MsgSubmitPocValidationsV2); ok {
			totalValidations += len(v.Validations)
		}
	}

	logging.Info("Broadcasting aggregated validation V2", types.Messages,
		"messages", len(aggregated),
		"totalValidations", totalValidations,
		"originalMessages", len(originalBatch))

	if err := c.txManager.SendBatchAsyncWithRetry(aggregated); err != nil {
		logging.Error("Failed to hand off aggregated validation V2 to TxManager", types.Messages, "error", err)
	}

	// Ack all original NATS messages
	for _, p := range originalBatch {
		p.natsMsg.Ack()
	}
}

func (c *BatchConsumer) unmarshalMsg(data []byte) (sdk.Msg, error) {
	var msg sdk.Msg
	if err := c.codec.UnmarshalInterfaceJSON(data, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *BatchConsumer) PublishPocValidationV2(msg sdk.Msg) error {
	return c.publishMsg(server.TxsBatchValidationV2Stream, msg)
}

func (c *BatchConsumer) publishMsg(stream string, msg sdk.Msg) error {
	data, err := c.codec.MarshalInterfaceJSON(msg)
	if err != nil {
		return err
	}
	_, err = c.js.Publish(stream, data)
	return err
}
