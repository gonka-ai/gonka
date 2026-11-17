package vocabulary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
	"golang.org/x/sync/singleflight"
)

const (
	huggingFaceBaseURL      = "https://huggingface.co"
	vocabularyFetchTimeout  = 10 * time.Second
	maxModelConfigBytes     = 1 << 20
	vocabularyRetryInterval = 10 * time.Minute
)

// ModelSource returns the Hugging Face repo and commit the chain pins for a model in an epoch.
type ModelSource interface {
	GetModelSource(ctx context.Context, epochID uint64, modelID string) (hfRepo, hfCommit string, err error)
}

// Resolver returns the vocab size of the model the chain pins for an (epoch, model), or 0 when it is unknown.
type Resolver interface {
	Resolve(ctx context.Context, epochID uint64, model string) int
}

type vocabularyCacheKey struct {
	epochID uint64
	model   string
}

type vocabularyCacheEntry struct {
	vocabularySize int
	retryAt        time.Time
}

// HuggingFaceResolver reads vocab_size from config.json at the pinned commit, the value vLLM bounds token ids by.
// A pinned commit never changes, so a resolved size is cached for good; a failure is retried after vocabularyRetryInterval.
type HuggingFaceResolver struct {
	sources    ModelSource
	baseURL    string
	httpClient *http.Client
	now        func() time.Time
	fetches    singleflight.Group

	mu    sync.Mutex
	cache map[vocabularyCacheKey]vocabularyCacheEntry
}

// NewResolver builds a resolver that looks the model up on chain and its config on the Hugging Face hub.
func NewResolver(sources ModelSource) *HuggingFaceResolver {
	return &HuggingFaceResolver{
		sources:    sources,
		baseURL:    huggingFaceBaseURL,
		httpClient: &http.Client{},
		now:        time.Now,
		cache:      make(map[vocabularyCacheKey]vocabularyCacheEntry),
	}
}

func (r *HuggingFaceResolver) Resolve(ctx context.Context, epochID uint64, model string) int {
	key := vocabularyCacheKey{epochID: epochID, model: model}
	if entry, cached := r.cachedEntry(key); cached && (entry.vocabularySize > 0 || r.now().Before(entry.retryAt)) {
		return entry.vocabularySize
	}

	// The fetch is shared by every validation waiting on this key, so it must outlive any single caller's cancellation.
	vocabularySize, _, _ := r.fetches.Do(fmt.Sprintf("%d/%s", epochID, model), func() (any, error) {
		fetchContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), vocabularyFetchTimeout)
		defer cancel()
		vocabularySize, err := r.fetchVocabularySize(fetchContext, epochID, model)
		entry := vocabularyCacheEntry{vocabularySize: vocabularySize}
		if err != nil {
			logging.Warn("model vocab size unknown; enforced token ids fall back to the coarse replay limit", types.Validation,
				"epoch", epochID, "model", model, "error", err)
			entry.retryAt = r.now().Add(vocabularyRetryInterval)
		}
		r.storeEntry(key, entry)
		return vocabularySize, nil
	})
	return vocabularySize.(int)
}

func (r *HuggingFaceResolver) cachedEntry(key vocabularyCacheKey) (vocabularyCacheEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, cached := r.cache[key]
	return entry, cached
}

func (r *HuggingFaceResolver) storeEntry(key vocabularyCacheKey, entry vocabularyCacheEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache[key].vocabularySize > 0 {
		return
	}
	r.cache[key] = entry
}

func (r *HuggingFaceResolver) fetchVocabularySize(ctx context.Context, epochID uint64, model string) (int, error) {
	hfRepo, hfCommit, err := r.sources.GetModelSource(ctx, epochID, model)
	if err != nil {
		return 0, fmt.Errorf("model source: %w", err)
	}
	if hfRepo == "" || hfCommit == "" {
		return 0, errors.New("model has no pinned hf_repo/hf_commit")
	}
	configURL, err := url.JoinPath(r.baseURL, hfRepo, "resolve", hfCommit, "config.json")
	if err != nil {
		return 0, fmt.Errorf("config url: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL, nil)
	if err != nil {
		return 0, fmt.Errorf("config request: %w", err)
	}
	response, err := r.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("fetch %s: %w", configURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fetch %s: status %d", configURL, response.StatusCode)
	}

	var config struct {
		VocabSize  int `json:"vocab_size"`
		TextConfig *struct {
			VocabSize int `json:"vocab_size"`
		} `json:"text_config"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxModelConfigBytes)).Decode(&config); err != nil {
		return 0, fmt.Errorf("decode %s: %w", configURL, err)
	}
	vocabularySize := config.VocabSize
	if config.TextConfig != nil {
		vocabularySize = config.TextConfig.VocabSize
	}
	if vocabularySize <= 0 {
		return 0, fmt.Errorf("%s has no vocab_size where vLLM reads it", configURL)
	}
	return vocabularySize, nil
}
