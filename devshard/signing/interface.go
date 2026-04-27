package signing

// Signer signs messages and provides the signer's address.
type Signer interface {
	Sign(message []byte) ([]byte, error)
	Address() string
}

// Verifier recovers the signer's address from a message and signature.
type Verifier interface {
	RecoverAddress(message []byte, signature []byte) (string, error)
}
