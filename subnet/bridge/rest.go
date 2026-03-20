package bridge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// warmCacheKey is the key for the warm key verification cache.
type warmCacheKey struct {
	host string
	warm string
}

// RESTBridge implements MainnetBridge query methods via the chain's grpc-gateway REST API.
// Notification and action methods return ErrNotImplemented.
type RESTBridge struct {
	baseURL   string
	client    *http.Client
	warmCache sync.Map // warmCacheKey -> bool
}

type Option func(*RESTBridge)

func WithHTTPClient(c *http.Client) Option {
	return func(b *RESTBridge) { b.client = c }
}

func NewRESTBridge(baseURL string, opts ...Option) *RESTBridge {
	b := &RESTBridge{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// -- response structs (unexported, match proto-JSON from grpc-gateway) --

type escrowResponse struct {
	Escrow struct {
		ID         uint64   `json:"id,string"`
		Creator    string   `json:"creator"`
		Amount     uint64   `json:"amount,string"`
		Slots      []string `json:"slots"`
		EpochIndex uint64   `json:"epoch_index,string"`
		AppHash    string   `json:"app_hash"`
		Settled    bool     `json:"settled"`
		TokenPrice uint64   `json:"token_price,string"`
	} `json:"escrow"`
	Found bool `json:"found"`
}

type participantResponse struct {
	Participant struct {
		Index        string `json:"index"`
		Address      string `json:"address"`
		InferenceURL string `json:"inference_url"`
		ValidatorKey string `json:"validator_key"` // base64-encoded
	} `json:"participant"`
}

type granteesResponse struct {
	Grantees []struct {
		Address string `json:"address"`
		PubKey  string `json:"pub_key"`
	} `json:"grantees"`
}

// -- helper --

func doGet[T any](client *http.Client, rawURL string) (*T, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("HTTP GET %s: status %d", rawURL, resp.StatusCode)
	}

	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response from %s: %w", rawURL, err)
	}
	return &result, nil
}

// -- query methods --

func (b *RESTBridge) GetEscrow(escrowID string) (*EscrowInfo, error) {
	u := fmt.Sprintf("%s/productscience/inference/inference/subnet_escrow/%s", b.baseURL, escrowID)

	resp, err := doGet[escrowResponse](b.client, u)
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.Found {
		return nil, ErrEscrowNotFound
	}

	appHash, err := hex.DecodeString(resp.Escrow.AppHash)
	if err != nil {
		return nil, fmt.Errorf("decode app_hash: %w", err)
	}

	return &EscrowInfo{
		EscrowID:       escrowID,
		Amount:         resp.Escrow.Amount,
		CreatorAddress: resp.Escrow.Creator,
		AppHash:        appHash,
		Slots:          resp.Escrow.Slots,
		TokenPrice:     resp.Escrow.TokenPrice,
	}, nil
}

func (b *RESTBridge) GetHostInfo(address string) (*HostInfo, error) {
	u := fmt.Sprintf("%s/productscience/inference/inference/participant/%s", b.baseURL, address)

	resp, err := doGet[participantResponse](b.client, u)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, ErrParticipantNotFound
	}

	return &HostInfo{
		Address: resp.Participant.Address,
		URL:     resp.Participant.InferenceURL,
	}, nil
}

const warmKeyMsgType = "/inference.inference.MsgStartInference"

func (b *RESTBridge) VerifyWarmKey(warmAddress, validatorAddress string) (bool, error) {
	key := warmCacheKey{host: validatorAddress, warm: warmAddress}
	if cached, ok := b.warmCache.Load(key); ok {
		return cached.(bool), nil
	}

	u := fmt.Sprintf("%s/productscience/inference/inference/grantees_by_message_type/%s/%s",
		b.baseURL, validatorAddress, url.PathEscape(warmKeyMsgType))

	resp, err := doGet[granteesResponse](b.client, u)
	if err != nil {
		return false, err
	}
	if resp == nil {
		b.warmCache.Store(key, false)
		return false, nil
	}

	found := false
	for _, g := range resp.Grantees {
		if g.Address == warmAddress {
			found = true
			break
		}
	}
	b.warmCache.Store(key, found)
	return found, nil
}

// -- stubs --

func (b *RESTBridge) OnEscrowCreated(_ EscrowInfo) error {
	return ErrNotImplemented
}

func (b *RESTBridge) OnSettlementProposed(_ string, _ []byte, _ uint64) error {
	return ErrNotImplemented
}

func (b *RESTBridge) OnSettlementFinalized(_ string) error {
	return ErrNotImplemented
}

func (b *RESTBridge) SubmitDisputeState(_ string, _ []byte, _ uint64, _ map[uint32][]byte) error {
	return ErrNotImplemented
}
