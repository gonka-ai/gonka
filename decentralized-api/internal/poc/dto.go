package poc

type InitDto struct {
	BlockHash      string  `json:"block_hash"`
	BlockHeight    int64   `json:"block_height"`
	PublicKey      string  `json:"public_key"`
	BatchSize      int     `json:"batch_size"`
	RTarget        float64 `json:"r_target"`
	FraudThreshold float64 `json:"fraud_threshold"`
	Params         *Params `json:"params"`
	NodeNum        int64   `json:"node_id"`
	TotalNodes     int64   `json:"node_count"`
	URL            string  `json:"url"`
}

type Params struct {
	Dim              int     `json:"dim"`
	NLayers          int     `json:"n_layers"`
	NHeads           int     `json:"n_heads"`
	NKVHeads         int     `json:"n_kv_heads"`
	VocabSize        int     `json:"vocab_size"`
	FFNDimMultiplier float64 `json:"ffn_dim_multiplier"`
	MultipleOf       int     `json:"multiple_of"`
	NormEps          float64 `json:"norm_eps"`
	RopeTheta        int     `json:"rope_theta"`
	UseScaledRope    bool    `json:"use_scaled_rope"`
	SeqLen           int     `json:"seq_len"`
}

type ProofBatch struct {
	PublicKey   string    `json:"public_key"`
	BlockHash   string    `json:"block_hash"`
	BlockHeight int64     `json:"block_height"`
	Nonces      []int64   `json:"nonces"`
	Dist        []float64 `json:"dist"`
}

type ValidatedBatch struct {
	ProofBatch // Inherits from ProofBatch

	// New fields
	ReceivedDist      []float64 `json:"received_dist"`
	RTarget           float64   `json:"r_target"`
	FraudThreshold    float64   `json:"fraud_threshold"`
	NInvalid          int64     `json:"n_invalid"`
	ProbabilityHonest float64   `json:"probability_honest"`
	FraudDetected     bool      `json:"fraud_detected"`
}

type InferenceUpDto struct {
	Model string   `json:"model"`
	Dtype string   `json:"dtype"`
	Args  []string `json:"additional_args"`
}
