package encclient

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/hpke"
	"github.com/cloudflare/circl/kem"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	EnvelopeVersion = 1

	KEMID  uint16 = 0x0020
	KDFID  uint16 = 0x0001
	AEADID uint16 = 0x0003

	X25519PublicKeySize  = 32
	X25519PrivateKeySize = 32

	ExportKeyLen     = 32
	ResponseNonceLen = chacha20poly1305.NonceSize

	RecipientKeyIDBytes  = 8
	RecipientKeyIDHexLen = RecipientKeyIDBytes * 2
)

var (
	HPKEInfo            = []byte("gonka/devshard/hpke-info/v1")
	aadLabelRequest     = []byte("gonka/devshard/req/v1")
	aadLabelResponse    = []byte("gonka/devshard/resp/v1")
	exportLabelResponse = []byte("gonka/devshard/response-key/v1")
	aadSeparator        = []byte{0x00}
)

type EncryptedRequest struct {
	Version        int    `json:"version"`
	SessionID      string `json:"session_id"`
	Nonce          uint64 `json:"nonce"`
	RecipientKeyID string `json:"recipient_key_id"`
	KEMID          uint16 `json:"kem_id"`
	KDFID          uint16 `json:"kdf_id"`
	AEADID         uint16 `json:"aead_id"`
	Enc            string `json:"enc"`
	PromptHash     string `json:"prompt_hash"`
	Ciphertext     string `json:"ciphertext"`
}

func RecipientKeyID(publicKey []byte) string {
	if len(publicKey) == 0 {
		return ""
	}
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:RecipientKeyIDBytes])
}

type EncryptedResponse struct {
	Version       int    `json:"version"`
	SessionID     string `json:"session_id"`
	Nonce         uint64 `json:"nonce"`
	AEADID        uint16 `json:"aead_id"`
	ResponseNonce string `json:"response_nonce"`
	Ciphertext    string `json:"ciphertext"`
}

func buildRequestAAD(sessionID string, nonce uint64, promptHash []byte) []byte {
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonce)
	return bytes.Join([][]byte{
		aadLabelRequest,
		[]byte(sessionID),
		nonceBytes,
		promptHash,
	}, aadSeparator)
}

func buildResponseAAD(sessionID string, nonce uint64) []byte {
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonce)
	return bytes.Join([][]byte{
		aadLabelResponse,
		[]byte(sessionID),
		nonceBytes,
	}, aadSeparator)
}

func suite() hpke.Suite {
	return hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_ChaCha20Poly1305)
}

func kemScheme() kem.Scheme {
	return hpke.KEM_X25519_HKDF_SHA256.Scheme()
}

func SealRequest(
	recipientPublicKey []byte,
	sessionID string,
	nonce uint64,
	plaintext []byte,
) (*EncryptedRequest, []byte, error) {
	if len(recipientPublicKey) != X25519PublicKeySize {
		return nil, nil, fmt.Errorf("encclient: recipient public key must be %d bytes, got %d",
			X25519PublicKeySize, len(recipientPublicKey))
	}
	pk, err := kemScheme().UnmarshalBinaryPublicKey(recipientPublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("encclient: parse recipient public key: %w", err)
	}

	sender, err := suite().NewSender(pk, HPKEInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("encclient: hpke sender setup: %w", err)
	}
	enc, sealer, err := sender.Setup(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("encclient: hpke sender setup: %w", err)
	}

	ph := sha256.Sum256(plaintext)
	aad := buildRequestAAD(sessionID, nonce, ph[:])
	ct, err := sealer.Seal(plaintext, aad)
	if err != nil {
		return nil, nil, fmt.Errorf("encclient: hpke seal: %w", err)
	}
	responseKey := sealer.Export(exportLabelResponse, ExportKeyLen)

	return &EncryptedRequest{
		Version:        EnvelopeVersion,
		SessionID:      sessionID,
		Nonce:          nonce,
		RecipientKeyID: RecipientKeyID(recipientPublicKey),
		KEMID:          KEMID,
		KDFID:          KDFID,
		AEADID:         AEADID,
		Enc:            base64.StdEncoding.EncodeToString(enc),
		PromptHash:     base64.StdEncoding.EncodeToString(ph[:]),
		Ciphertext:     base64.StdEncoding.EncodeToString(ct),
	}, responseKey, nil
}

func OpenResponse(responseKey []byte, resp *EncryptedResponse) ([]byte, error) {
	if resp == nil {
		return nil, errors.New("encclient: nil EncryptedResponse")
	}
	if resp.Version != EnvelopeVersion {
		return nil, fmt.Errorf("encclient: unsupported response envelope version %d", resp.Version)
	}
	if resp.AEADID != AEADID {
		return nil, fmt.Errorf("encclient: unsupported response AEAD id 0x%04x", resp.AEADID)
	}
	if len(responseKey) != ExportKeyLen {
		return nil, fmt.Errorf("encclient: response_key must be %d bytes, got %d",
			ExportKeyLen, len(responseKey))
	}

	responseNonce, err := base64.StdEncoding.DecodeString(resp.ResponseNonce)
	if err != nil {
		return nil, fmt.Errorf("encclient: decode response_nonce: %w", err)
	}
	if len(responseNonce) != ResponseNonceLen {
		return nil, fmt.Errorf("encclient: response_nonce must be %d bytes, got %d",
			ResponseNonceLen, len(responseNonce))
	}
	ct, err := base64.StdEncoding.DecodeString(resp.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("encclient: decode response ciphertext: %w", err)
	}

	aead, err := chacha20poly1305.New(responseKey)
	if err != nil {
		return nil, fmt.Errorf("encclient: chacha20poly1305 init: %w", err)
	}
	pt, err := aead.Open(nil, responseNonce, ct, buildResponseAAD(resp.SessionID, resp.Nonce))
	if err != nil {
		return nil, fmt.Errorf("encclient: open response: %w", err)
	}
	return pt, nil
}
