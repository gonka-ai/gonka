package transport

const (
	hostCatchUpReserveBytes               int64 = 512 * 1024
	inferenceRequestEnvelopeOverheadBytes int64 = 1024

	MaxRawPromptBytes int64 = (DefaultMaxBodySize - hostCatchUpReserveBytes - inferenceRequestEnvelopeOverheadBytes) / 4 * 3
)

type inferenceRequestSize struct {
	PromptBytes int64
	DiffsBytes  int64
	DiffCount   int
}

func measureInferenceRequest(request InferenceRequest) inferenceRequestSize {
	size := inferenceRequestSize{DiffCount: len(request.Diffs)}
	if request.Payload != nil {
		size.PromptBytes = int64(len(request.Payload.Prompt))
	}
	for _, diff := range request.Diffs {
		size.DiffsBytes += int64(len(diff.Txs) + len(diff.UserSig) + len(diff.PostStateRoot))
	}
	return size
}
