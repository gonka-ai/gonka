package mlnodeclient

import (
	"context"
	"decentralized-api/encclient"
	"decentralized-api/utils"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const encryptedIdentityPath = "/api/v1/encrypted/identity"

type EncryptedIdentity struct {
	Provider        string
	EnvelopeVersion string
	PublicKey       []byte

	KeyID       string
	Quote       []byte
	Measurement string
	Extra       map[string]any
}

type encryptedIdentityWire struct {
	Provider        string         `json:"provider"`
	EnvelopeVersion string         `json:"envelope_version"`
	PublicKey       string         `json:"public_key"`
	KeyID           string         `json:"key_id,omitempty"`
	Quote           string         `json:"quote,omitempty"`
	Measurement     string         `json:"measurement,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

func (api *Client) GetEncryptedIdentity(ctx context.Context) (*EncryptedIdentity, error) {
	requestURL, err := url.JoinPath(api.pocUrl, encryptedIdentityPath)
	if err != nil {
		return nil, fmt.Errorf("build encrypted identity url: %w", err)
	}

	resp, err := utils.SendGetRequest(ctx, &api.client, requestURL)
	if err != nil {
		return nil, fmt.Errorf("encrypted identity request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, ErrEncryptedIdentityDisabled
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("encrypted identity returned status %d", resp.StatusCode)
	}

	var wire encryptedIdentityWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode encrypted identity: %w", err)
	}

	publicKey, err := base64.StdEncoding.DecodeString(wire.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("decode public_key base64: %w", err)
	}
	var quote []byte
	if wire.Quote != "" {
		quote, err = base64.StdEncoding.DecodeString(wire.Quote)
		if err != nil {
			return nil, fmt.Errorf("decode quote base64: %w", err)
		}
	}

	keyID := encclient.RecipientKeyID(publicKey)
	if wire.KeyID != "" && wire.KeyID != keyID {
		return nil, fmt.Errorf(
			"encrypted identity advertised key_id %q does not match sha256(public_key)=%q",
			wire.KeyID, keyID,
		)
	}

	identity := &EncryptedIdentity{
		Provider:        wire.Provider,
		EnvelopeVersion: wire.EnvelopeVersion,
		PublicKey:       publicKey,
		KeyID:           keyID,
		Quote:           quote,
		Measurement:     wire.Measurement,
		Extra:           wire.Extra,
	}
	if identity.EnvelopeVersion == "" {

		identity.EnvelopeVersion = "v1"
	}
	return identity, nil
}

var ErrEncryptedIdentityDisabled = fmt.Errorf("ml node encrypted identity disabled")
