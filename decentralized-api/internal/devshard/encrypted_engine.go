package devshard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"decentralized-api/encclient"
	devshardpkg "devshard"
	mlnodegen "devshard/mlnode/gen"
	mlnodepkg "devshard/mlnode"
)

const encryptedMLPath = "/api/v1/encrypted/chat/completions"
const encryptedReleaseTimeout = 3 * time.Second

type mlNodeAcquirer interface {
	AcquireByKey(ctx context.Context, recipientKeyID string) (*mlnodegen.AcquireMLNodeResponse, error)
	Release(ctx context.Context, lockID string, outcome mlnodegen.ReleaseOutcome) error
}

type encryptedEngine struct {
	mlClient   mlNodeAcquirer
	httpClient *http.Client
}

func NewEncryptedEngine(mlClient mlNodeAcquirer, httpClient *http.Client) devshardpkg.EncryptedEngine {
	return &encryptedEngine{mlClient: mlClient, httpClient: httpClient}
}

func (e *encryptedEngine) ForwardEncrypted(ctx context.Context, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty body", devshardpkg.EncryptedBadEnvelope)
	}
	keyID, err := recipientKeyIDFromEnvelope(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", devshardpkg.EncryptedBadEnvelope, err)
	}

	acq, err := e.mlClient.AcquireByKey(ctx, keyID)
	if err != nil {
		if errors.Is(err, mlnodepkg.ErrRecipientKeyUnknown) {
			return nil, fmt.Errorf("%w: %s", devshardpkg.EncryptedKeyUnknown, keyID)
		}
		return nil, fmt.Errorf("%w: %v", devshardpkg.EncryptedNoCapacity, err)
	}

	respBody, status, callErr := e.postEncrypted(ctx, acq.Endpoint, body)
	outcome, forwardErr := classifyForwardOutcome(respBody, status, callErr)

	releaseCtx, cancel := context.WithTimeout(context.Background(), encryptedReleaseTimeout)
	defer cancel()
	if releaseErr := e.mlClient.Release(releaseCtx, acq.LockId, outcome); releaseErr != nil && forwardErr == nil {
		forwardErr = fmt.Errorf("release: %w", releaseErr)
	}
	if forwardErr != nil {
		return nil, forwardErr
	}
	return respBody, nil
}

func classifyForwardOutcome(body []byte, status int, callErr error) (mlnodegen.ReleaseOutcome, error) {
	switch {
	case callErr != nil:
		return mlnodegen.ReleaseOutcome_TRANSPORT_ERROR, callErr
	case status >= 500:
		return mlnodegen.ReleaseOutcome_TRANSPORT_ERROR,
			fmt.Errorf("upstream status %d: %s", status, truncate(body, 512))
	case status == http.StatusMisdirectedRequest:
		return mlnodegen.ReleaseOutcome_APPLICATION_ERROR,
			fmt.Errorf("upstream status %d (key mismatch on targeted node): %s",
				status, truncate(body, 512))
	case status >= 400:
		return mlnodegen.ReleaseOutcome_APPLICATION_ERROR,
			fmt.Errorf("upstream status %d: %s", status, truncate(body, 512))
	}
	return mlnodegen.ReleaseOutcome_SUCCESS, nil
}

func recipientKeyIDFromEnvelope(body []byte) (string, error) {
	var head struct {
		RecipientKeyID string `json:"recipient_key_id"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return "", fmt.Errorf("envelope head: %w", err)
	}
	if len(head.RecipientKeyID) != encclient.RecipientKeyIDHexLen {
		return "", fmt.Errorf("recipient_key_id must be %d hex chars, got %d",
			encclient.RecipientKeyIDHexLen, len(head.RecipientKeyID))
	}
	for i := 0; i < len(head.RecipientKeyID); i++ {
		c := head.RecipientKeyID[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("recipient_key_id must be lowercase hex")
		}
	}
	return head.RecipientKeyID, nil
}

func (e *encryptedEngine) postEncrypted(ctx context.Context, endpoint string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+encryptedMLPath, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

var _ devshardpkg.EncryptedEngine = (*encryptedEngine)(nil)
