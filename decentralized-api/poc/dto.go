package poc

type ProofBatch struct {
	PublicKey   string    `json:"public_key"`
	ChainHash   string    `json:"chain_hash"`
	ChainHeight int64     `json:"chain_height"`
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
