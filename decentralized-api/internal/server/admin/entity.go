package admin

type UnitOfComputePriceProposalDto struct {
	Price uint64 `json:"price"`
	Denom string `json:"denom"`
}

type RegisterModelDto struct {
	Id                     string `json:"id"`
	ContextInAiTokens      uint64 `json:"context_in_ai_tokens"`
	Quantization           string `json:"quantization"`
	InputPriceInAiTokens   uint64 `json:"input_price_in_ai_tokens"`
	OutputPriceInAiTokens  uint64 `json:"output_price_in_ai_tokens"`
	UnitsOfComputePerToken uint64 `json:"units_of_compute_per_token"`
}
