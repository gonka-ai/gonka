package tx_manager

// defaultMaxBlocks is the default deadline in blocks (500 blocks ≈ 40 minutes at 5s/block)
const defaultMaxBlocks int64 = 30

// deadlineByMsgType defines message-specific deadlines based on chain epoch parameters
var deadlineByMsgType = map[string]int64{
	"/inference.inference.MsgSubmitPocBatch":      240,
	"/inference.inference.MsgSubmitPocValidation": 240,
	"/inference.inference.MsgFinishInference":     150,
	"/inference.inference.MsgValidation":          150,
	"/inference.inference.MsgStartInference":      150,
}

// getMaxBlocksForType returns the maximum blocks a transaction type can wait before expiring.
// Returns defaultMaxBlocks if the message type is not in the configuration.
func getMaxBlocksForType(msgType string) int64 {
	if blocks, ok := deadlineByMsgType[msgType]; ok {
		return blocks
	}
	return defaultMaxBlocks
}
