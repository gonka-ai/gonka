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

	"devshard/types"
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
		TokenPrice                uint64 `json:"token_price,string"`
		CreateDevshardFee         uint64 `json:"create_devshard_fee,string"`
		FeePerNonce               uint64 `json:"fee_per_nonce,string"`
		InferenceSealGraceNonces  uint32 `json:"inference_seal_grace_nonces"`
		InferenceSealGraceSeconds uint32 `json:"inference_seal_grace_seconds"`
		AutoSealEveryNNonces      uint32 `json:"auto_seal_every_n_nonces"`
		ValidationRate            uint32 `json:"validation_rate"`
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

type epochGroupDataResponse struct {
	EpochGroupData struct {
		ModelSnapshot *struct {
			ValidationThreshold *Decimal `json:"validation_threshold"`
		} `json:"model_snapshot"`
	} `json:"epoch_group_data"`
}

// paramsResponse matches grpc-gateway JSON for QueryParams (inference module).
type paramsResponse struct {
	Params *struct {
		DevshardEscrowParams *struct {
			RefusalTimeout      int64  `json:"refusal_timeout,string"`
			ExecutionTimeout    int64  `json:"execution_timeout,string"`
			ValidationRate      uint32 `json:"validation_rate"`
			VoteThresholdFactor uint32 `json:"vote_threshold_factor"`
		} `json:"devshard_escrow_params"`
	} `json:"params"`
}

var _ SessionBindParamsBridge = (*RESTBridge)(nil)

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
	u := fmt.Sprintf("%s/productscience/inference/inference/devshard_escrow/%s", b.baseURL, escrowID)

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
		EscrowID:                  escrowID,
		Amount:                    resp.Escrow.Amount,
		CreatorAddress:            resp.Escrow.Creator,
		AppHash:                   appHash,
		Slots:                     resp.Escrow.Slots,
		TokenPrice:                resp.Escrow.TokenPrice,
		CreateDevshardFee:         resp.Escrow.CreateDevshardFee,
		FeePerNonce:               resp.Escrow.FeePerNonce,
		InferenceSealGraceNonces:  resp.Escrow.InferenceSealGraceNonces,
		InferenceSealGraceSeconds: resp.Escrow.InferenceSealGraceSeconds,
		AutoSealEveryNNonces:      resp.Escrow.AutoSealEveryNNonces,
		ValidationRate:            resp.Escrow.ValidationRate,
		EpochID:                   resp.Escrow.EpochIndex,
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

func (b *RESTBridge) GetSessionBindParams() (types.LiveSessionBindParams, error) {
	u := fmt.Sprintf("%s/productscience/inference/inference/params", b.baseURL)

	resp, err := doGet[paramsResponse](b.client, u)
	if err != nil {
		return types.LiveSessionBindParams{}, err
	}
	if resp == nil || resp.Params == nil || resp.Params.DevshardEscrowParams == nil {
		return types.LiveSessionBindParams{}, fmt.Errorf("devshard escrow params missing from chain params response")
	}
	dep := resp.Params.DevshardEscrowParams
	return types.LiveSessionBindParams{
		RefusalTimeout:      dep.RefusalTimeout,
		ExecutionTimeout:    dep.ExecutionTimeout,
		ValidationRate:      dep.ValidationRate,
		VoteThresholdFactor: dep.VoteThresholdFactor,
	}, nil
}

func (b *RESTBridge) GetValidationThreshold(epochID uint64, modelID string) (*Decimal, error) {
	u := fmt.Sprintf("%s/productscience/inference/inference/epoch_group_data/%d?model_id=%s",
		b.baseURL, epochID, url.QueryEscape(modelID))

	resp, err := doGet[epochGroupDataResponse](b.client, u)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.EpochGroupData.ModelSnapshot == nil || resp.EpochGroupData.ModelSnapshot.ValidationThreshold == nil {
		return nil, fmt.Errorf("validation threshold not found for epoch %d model %s", epochID, modelID)
	}
	threshold := *resp.EpochGroupData.ModelSnapshot.ValidationThreshold
	return &threshold, nil
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
