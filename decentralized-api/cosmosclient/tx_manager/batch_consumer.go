package tx_manager

import (
	"decentralized-api/internal/nats/server"
	"decentralized-api/logging"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/x/inference/types"
)

const (
	batchStartConsumer        = "batch-start-consumer"
	batchFinishConsumer       = "batch-finish-consumer"
	batchValidationV2Consumer = "batch-validation-v2-consumer"
	batchAckWait              = time.Minute // must exceed FlushTimeout to prevent redelivery

	// V1 PoC batch consumers
	batchPocBatchConsumer      = "batch-poc-batch-consumer"
	batchPocValidationConsumer = "batch-poc-validation-consumer"
)

type BatchConfig struct {
	FlushSize                int
	FlushTimeout             time.Duration
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

	startBatch        []pendingMsg
	finishBatch       []pendingMsg
	validationV2Batch []pendingMsg

	// V1 PoC batches
	pocBatchBatch      []pendingMsg
	pocValidationBatch []pendingMsg

	startMu        sync.Mutex
	finishMu       sync.Mutex
	validationV2Mu sync.Mutex

	// V1 PoC mutexes
	pocBatchMu      sync.Mutex
	pocValidationMu sync.Mutex

	startCreatedAt        time.Time
	finishCreatedAt       time.Time
	validationV2CreatedAt time.Time

	// V1 PoC timestamps
	pocBatchCreatedAt      time.Time
	pocValidationCreatedAt time.Time
}

func NewBatchConsumer(
	js nats.JetStreamContext,
	cdc codec.Codec,
	txManager TxManager,
	config BatchConfig,
) *BatchConsumer {
	return &BatchConsumer{
		js:                 js,
		codec:              cdc,
		txManager:          txManager,
		config:             config,
		startBatch:         make([]pendingMsg, 0, config.FlushSize),
		finishBatch:        make([]pendingMsg, 0, config.FlushSize),
		validationV2Batch:  make([]pendingMsg, 0, config.ValidationV2FlushSize),
		pocBatchBatch:      make([]pendingMsg, 0, config.FlushSize),
		pocValidationBatch: make([]pendingMsg, 0, config.FlushSize),
	}
}

func (c *BatchConsumer) Start() error {
	if err := c.subscribeStream(server.TxsBatchStartStream, batchStartConsumer, c.handleStartMsg); err != nil {
		return err
	}
	if err := c.subscribeStream(server.TxsBatchFinishStream, batchFinishConsumer, c.handleFinishMsg); err != nil {
		return err
	}
	if err := c.subscribeStream(server.TxsBatchValidationV2Stream, batchValidationV2Consumer, c.handleValidationV2Msg); err != nil {
		return err
	}
	// V1 PoC streams
	if err := c.subscribeStream(server.TxsBatchPocBatchStream, batchPocBatchConsumer, c.handlePocBatchMsg); err != nil {
		return err
	}
	if err := c.subscribeStream(server.TxsBatchPocValidationStream, batchPocValidationConsumer, c.handlePocValidationMsg); err != nil {
		return err
	}

	go c.flushLoop()
	logging.Info("Batch consumer started", types.Messages,
		"flushSize", c.config.FlushSize,
		"flushTimeout", c.config.FlushTimeout)
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

func (c *BatchConsumer) handleStartMsg(msg *nats.Msg) {
	if err := msg.InProgress(); err != nil {
		logging.Error("Failed to mark start msg in progress", types.Messages, "error", err)
	}
	sdkMsg, err := c.unmarshalMsg(msg.Data)
	if err != nil {
		logging.Error("Failed to unmarshal start msg", types.Messages, "error", err)
		msg.Term()
		return
	}

	var shouldFlush bool
	c.startMu.Lock()
	if len(c.startBatch) == 0 {
		c.startCreatedAt = time.Now()
	}
	c.startBatch = append(c.startBatch, pendingMsg{msg: sdkMsg, natsMsg: msg})
	shouldFlush = len(c.startBatch) >= c.config.FlushSize
	c.startMu.Unlock()

	if shouldFlush {
		c.flushStart()
	}
}

func (c *BatchConsumer) handleFinishMsg(msg *nats.Msg) {
	if err := msg.InProgress(); err != nil {
		logging.Error("Failed to mark finish msg in progress", types.Messages, "error", err)
	}
	sdkMsg, err := c.unmarshalMsg(msg.Data)
	if err != nil {
		logging.Error("Failed to unmarshal finish msg", types.Messages, "error", err)
		msg.Term()
		return
	}

	var shouldFlush bool
	c.finishMu.Lock()
	if len(c.finishBatch) == 0 {
		c.finishCreatedAt = time.Now()
	}
	c.finishBatch = append(c.finishBatch, pendingMsg{msg: sdkMsg, natsMsg: msg})
	shouldFlush = len(c.finishBatch) >= c.config.FlushSize
	c.finishMu.Unlock()

	if shouldFlush {
		c.flushFinish()
	}
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

func (c *BatchConsumer) handlePocBatchMsg(msg *nats.Msg) {
	if err := msg.InProgress(); err != nil {
		logging.Error("Failed to mark poc batch msg in progress", types.Messages, "error", err)
	}
	sdkMsg, err := c.unmarshalMsg(msg.Data)
	if err != nil {
		logging.Error("Failed to unmarshal poc batch msg", types.Messages, "error", err)
		msg.Term()
		return
	}

	var shouldFlush bool
	c.pocBatchMu.Lock()
	if len(c.pocBatchBatch) == 0 {
		c.pocBatchCreatedAt = time.Now()
	}
	c.pocBatchBatch = append(c.pocBatchBatch, pendingMsg{msg: sdkMsg, natsMsg: msg})
	shouldFlush = len(c.pocBatchBatch) >= c.config.FlushSize
	c.pocBatchMu.Unlock()

	if shouldFlush {
		c.flushPocBatch()
	}
}

func (c *BatchConsumer) handlePocValidationMsg(msg *nats.Msg) {
	if err := msg.InProgress(); err != nil {
		logging.Error("Failed to mark poc validation msg in progress", types.Messages, "error", err)
	}
	sdkMsg, err := c.unmarshalMsg(msg.Data)
	if err != nil {
		logging.Error("Failed to unmarshal poc validation msg", types.Messages, "error", err)
		msg.Term()
		return
	}

	var shouldFlush bool
	c.pocValidationMu.Lock()
	if len(c.pocValidationBatch) == 0 {
		c.pocValidationCreatedAt = time.Now()
	}
	c.pocValidationBatch = append(c.pocValidationBatch, pendingMsg{msg: sdkMsg, natsMsg: msg})
	shouldFlush = len(c.pocValidationBatch) >= c.config.FlushSize
	c.pocValidationMu.Unlock()

	if shouldFlush {
		c.flushPocValidation()
	}
}

func (c *BatchConsumer) flushLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.extendAckDeadlines()
		c.checkAndFlushStart()
		c.checkAndFlushFinish()
		c.checkAndFlushValidationV2()
		c.checkAndFlushPocBatch()
		c.checkAndFlushPocValidation()
	}
}

func (c *BatchConsumer) extendAckDeadlines() {
	c.startMu.Lock()
	for _, p := range c.startBatch {
		_ = p.natsMsg.InProgress()
	}
	c.startMu.Unlock()

	c.finishMu.Lock()
	for _, p := range c.finishBatch {
		_ = p.natsMsg.InProgress()
	}
	c.finishMu.Unlock()

	c.validationV2Mu.Lock()
	for _, p := range c.validationV2Batch {
		_ = p.natsMsg.InProgress()
	}
	c.validationV2Mu.Unlock()

	c.pocBatchMu.Lock()
	for _, p := range c.pocBatchBatch {
		_ = p.natsMsg.InProgress()
	}
	c.pocBatchMu.Unlock()

	c.pocValidationMu.Lock()
	for _, p := range c.pocValidationBatch {
		_ = p.natsMsg.InProgress()
	}
	c.pocValidationMu.Unlock()
}

func (c *BatchConsumer) checkAndFlushStart() {
	c.startMu.Lock()
	shouldFlush := len(c.startBatch) > 0 && time.Since(c.startCreatedAt) >= c.config.FlushTimeout
	c.startMu.Unlock()

	if shouldFlush {
		c.flushStart()
	}
}

func (c *BatchConsumer) checkAndFlushFinish() {
	c.finishMu.Lock()
	shouldFlush := len(c.finishBatch) > 0 && time.Since(c.finishCreatedAt) >= c.config.FlushTimeout
	c.finishMu.Unlock()

	if shouldFlush {
		c.flushFinish()
	}
}

func (c *BatchConsumer) checkAndFlushValidationV2() {
	c.validationV2Mu.Lock()
	shouldFlush := len(c.validationV2Batch) > 0 && time.Since(c.validationV2CreatedAt) >= c.config.ValidationV2FlushTimeout
	c.validationV2Mu.Unlock()

	if shouldFlush {
		c.flushValidationV2()
	}
}

func (c *BatchConsumer) checkAndFlushPocBatch() {
	c.pocBatchMu.Lock()
	shouldFlush := len(c.pocBatchBatch) > 0 && time.Since(c.pocBatchCreatedAt) >= c.config.FlushTimeout
	c.pocBatchMu.Unlock()

	if shouldFlush {
		c.flushPocBatch()
	}
}

func (c *BatchConsumer) checkAndFlushPocValidation() {
	c.pocValidationMu.Lock()
	shouldFlush := len(c.pocValidationBatch) > 0 && time.Since(c.pocValidationCreatedAt) >= c.config.FlushTimeout
	c.pocValidationMu.Unlock()

	if shouldFlush {
		c.flushPocValidation()
	}
}

func (c *BatchConsumer) flushStart() {
	c.startMu.Lock()
	batch := c.startBatch
	if len(batch) == 0 {
		c.startMu.Unlock()
		return
	}
	c.startBatch = make([]pendingMsg, 0, c.config.FlushSize)
	c.startCreatedAt = time.Time{} // reset timer
	c.startMu.Unlock()

	c.broadcastBatch("start", batch)
}

func (c *BatchConsumer) flushFinish() {
	c.finishMu.Lock()
	batch := c.finishBatch
	if len(batch) == 0 {
		c.finishMu.Unlock()
		return
	}
	c.finishBatch = make([]pendingMsg, 0, c.config.FlushSize)
	c.finishCreatedAt = time.Time{} // reset timer
	c.finishMu.Unlock()

	c.broadcastBatch("finish", batch)
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

func (c *BatchConsumer) flushPocBatch() {
	c.pocBatchMu.Lock()
	batch := c.pocBatchBatch
	if len(batch) == 0 {
		c.pocBatchMu.Unlock()
		return
	}
	c.pocBatchBatch = make([]pendingMsg, 0, c.config.FlushSize)
	c.pocBatchCreatedAt = time.Time{} // reset timer
	c.pocBatchMu.Unlock()

	c.broadcastBatch("poc-batch", batch)
}

func (c *BatchConsumer) flushPocValidation() {
	c.pocValidationMu.Lock()
	batch := c.pocValidationBatch
	if len(batch) == 0 {
		c.pocValidationMu.Unlock()
		return
	}
	c.pocValidationBatch = make([]pendingMsg, 0, c.config.FlushSize)
	c.pocValidationCreatedAt = time.Time{} // reset timer
	c.pocValidationMu.Unlock()

	c.broadcastBatch("poc-validation", batch)
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

func (c *BatchConsumer) broadcastBatch(batchType string, batch []pendingMsg) {
	msgs := make([]sdk.Msg, len(batch))
	for i, p := range batch {
		msgs[i] = p.msg
	}

	logging.Info("Broadcasting batch", types.Messages, "type", batchType, "count", len(msgs))

	if err := c.txManager.SendBatchAsyncWithRetry(msgs); err != nil {
		logging.Error("Failed to hand off batch to TxManager", types.Messages, "type", batchType, "error", err)
	}

	for _, p := range batch {
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

func (c *BatchConsumer) PublishStartInference(msg sdk.Msg) error {
	return c.publishMsg(server.TxsBatchStartStream, msg)
}

func (c *BatchConsumer) PublishFinishInference(msg sdk.Msg) error {
	return c.publishMsg(server.TxsBatchFinishStream, msg)
}

func (c *BatchConsumer) PublishPocValidationV2(msg sdk.Msg) error {
	return c.publishMsg(server.TxsBatchValidationV2Stream, msg)
}

// V1 PoC publish methods
func (c *BatchConsumer) PublishPocBatch(msg sdk.Msg) error {
	return c.publishMsg(server.TxsBatchPocBatchStream, msg)
}

func (c *BatchConsumer) PublishPocValidation(msg sdk.Msg) error {
	return c.publishMsg(server.TxsBatchPocValidationStream, msg)
}

func (c *BatchConsumer) publishMsg(stream string, msg sdk.Msg) error {
	data, err := c.codec.MarshalInterfaceJSON(msg)
	if err != nil {
		return err
	}
	_, err = c.js.Publish(stream, data)
	return err
}
