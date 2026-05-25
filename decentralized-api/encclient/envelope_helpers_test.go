package encclient

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

func generateKeyPair() (publicKey, privateKey []byte, err error) {
	pub, priv, err := kemScheme().GenerateKeyPair()
	if err != nil {
		return nil, nil, err
	}
	pubBytes, err := pub.MarshalBinary()
	if err != nil {
		return nil, nil, err
	}
	privBytes, err := priv.MarshalBinary()
	if err != nil {
		return nil, nil, err
	}
	return pubBytes, privBytes, nil
}

func sealResponseForTest(
	responseKey []byte,
	sessionID string,
	nonce uint64,
	plaintext []byte,
) (*EncryptedResponse, error) {
	if len(responseKey) != ExportKeyLen {
		return nil, fmt.Errorf("encclient(test): response_key must be %d bytes", ExportKeyLen)
	}
	responseNonce := make([]byte, ResponseNonceLen)
	if _, err := rand.Read(responseNonce); err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.New(responseKey)
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, responseNonce, plaintext, buildResponseAAD(sessionID, nonce))
	return &EncryptedResponse{
		Version:       EnvelopeVersion,
		SessionID:     sessionID,
		Nonce:         nonce,
		AEADID:        AEADID,
		ResponseNonce: base64.StdEncoding.EncodeToString(responseNonce),
		Ciphertext:    base64.StdEncoding.EncodeToString(ct),
	}, nil
}

func openRequestForTest(
	recipientPrivateKey []byte,
	req *EncryptedRequest,
) (plaintext, responseKey []byte, err error) {
	if req == nil {
		return nil, nil, errors.New("encclient(test): nil EncryptedRequest")
	}
	if req.Version != EnvelopeVersion {
		return nil, nil, fmt.Errorf("encclient(test): unsupported envelope version %d", req.Version)
	}
	if req.KEMID != KEMID || req.KDFID != KDFID || req.AEADID != AEADID {
		return nil, nil, fmt.Errorf("encclient(test): unsupported HPKE suite")
	}
	if len(recipientPrivateKey) != X25519PrivateKeySize {
		return nil, nil, fmt.Errorf("encclient(test): bad private key length")
	}
	sk, err := kemScheme().UnmarshalBinaryPrivateKey(recipientPrivateKey)
	if err != nil {
		return nil, nil, err
	}
	enc, err := base64.StdEncoding.DecodeString(req.Enc)
	if err != nil {
		return nil, nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(req.Ciphertext)
	if err != nil {
		return nil, nil, err
	}
	ph, err := base64.StdEncoding.DecodeString(req.PromptHash)
	if err != nil {
		return nil, nil, err
	}
	receiver, err := suite().NewReceiver(sk, HPKEInfo)
	if err != nil {
		return nil, nil, err
	}
	opener, err := receiver.Setup(enc)
	if err != nil {
		return nil, nil, err
	}
	pt, err := opener.Open(ct, buildRequestAAD(req.SessionID, req.Nonce, ph))
	if err != nil {
		return nil, nil, err
	}

	got := sha256.Sum256(pt)
	if !bytes.Equal(got[:], ph) {
		return nil, nil, errors.New("encclient(test): prompt hash does not match decrypted body")
	}
	return pt, opener.Export(exportLabelResponse, ExportKeyLen), nil
}
