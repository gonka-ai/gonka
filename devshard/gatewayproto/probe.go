package gatewayproto

import (
	"devshard/host"
	"devshard/user"
)

// ProbeParams changes only the synthetic request's body and reservation. The
// adapter supplies the existing model, timestamp, prompt and token budget.
func ProbeParams(params user.InferenceParams, prompt []byte, maxTokens uint64) user.InferenceParams {
	params.Prompt = prompt
	params.InputLength = uint64(len(prompt))
	params.MaxTokens = maxTokens
	return params
}

func ExecutorAcknowledged(response *host.HostResponse) bool {
	return response != nil && len(response.Receipt) > 0
}

// ApplyProbeResponse preserves the probe-specific send-error guard. Receipt
// callbacks survive a failed send, but reply receipts are read only on success,
// before applying the reply. Ordinary inference dispatch has a separate policy.
func ApplyProbeResponse(reply *host.HostResponse, sendErr error, acknowledged bool, apply func() error) (bool, error) {
	if sendErr != nil {
		return acknowledged, sendErr
	}
	acknowledged = acknowledged || ExecutorAcknowledged(reply)
	return acknowledged, apply()
}
