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

// PeerSessionKeyDeriver derives the HMAC key for peer RPC session tokens.
// Replicas that hold the same host private key derive the same key. A signer
// that cannot derive one must not issue tokens.
type PeerSessionKeyDeriver interface {
	DerivePeerSessionKey(host, version string, keyID byte) ([]byte, error)
}
