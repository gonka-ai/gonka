// Package tokenizer counts a prompt's tokens with the tokenizer.json a model's pinned Hugging Face revision ships; see README.md.
package tokenizer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

// See README.md, "Where the tokenizer comes from" and "Bounds".
const (
	maxTokenizerBytes = 64 << 20
	loadTimeout       = 2 * time.Minute
	retryInterval     = 10 * time.Minute
	unpinned          = ""
)

var (
	errNoTokenizer   = errors.New("pinned revision has no tokenizer.json")
	errParsePanicked = errors.New("tokenizer.json parse panicked")
)

// ModelSource returns the Hugging Face repo and commit the chain pins for a model in an epoch; vocabulary.ChainModelSource satisfies it.
type ModelSource interface {
	GetModelSource(ctx context.Context, epochID uint64, modelID string) (hfRepo, hfCommit string, err error)
}

// Library holds the tokenizer the chain pins for each model it is asked about, loading each in the background on first use.
type Library struct {
	source     ModelSource
	epoch      func() (epoch uint64, known bool)
	httpClient *http.Client
	now        func() time.Time
	background func(task func())
	parse      func(io.Reader) (*Counter, error)

	mu     sync.Mutex
	models map[string]*pinnedTokenizer
}

type revision struct{ repo, commit string }

func (r revision) String() string { return r.repo + "@" + r.commit }

type pinnedTokenizer struct {
	epoch    uint64
	revision revision
	counter  *Counter
	loading  bool
	retryAt  time.Time
}

func (p *pinnedTokenizer) startRefresh(epoch uint64, now time.Time) bool {
	if p.epoch == epoch || p.loading || now.Before(p.retryAt) {
		return false
	}
	p.loading = true
	return true
}

func (p *pinnedTokenizer) settle(epoch uint64, pinned revision, counter *Counter) {
	p.epoch, p.revision, p.counter, p.loading = epoch, pinned, counter, false
}

func (p *pinnedTokenizer) retryAfter(at time.Time, pinMoved bool) {
	p.loading, p.retryAt = false, at
	if pinMoved {
		p.counter = nil
	}
}

// NewLibrary builds a library that reads each model's pin on chain for the epoch the epoch func reports, nothing while it knows none, and the tokenizer from the Hugging Face hub.
func NewLibrary(source ModelSource, epoch func() (epoch uint64, known bool)) *Library {
	return &Library{
		source:     source,
		epoch:      epoch,
		httpClient: &http.Client{},
		now:        time.Now,
		background: func(task func()) { go task() },
		parse:      Load,
		models:     make(map[string]*pinnedTokenizer),
	}
}

// Counter returns the model's loaded tokenizer, nil while it has none, and starts reading the model's pin when the epoch moved on or a retry is due.
func (l *Library) Counter(model string) *Counter {
	epoch, known := l.epoch()
	if !known {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, found := l.models[model]
	if !found {
		entry = &pinnedTokenizer{}
		l.models[model] = entry
	}
	if entry.startRefresh(epoch, l.now()) {
		held := entry.revision
		l.background(func() { l.refresh(model, epoch, held) })
	}
	return entry.counter
}

func (l *Library) refresh(model string, epoch uint64, held revision) {
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	started := l.now()
	pinned, err := l.readPin(ctx, model, epoch)
	if err == nil && pinned == held {
		l.update(model, func(entry *pinnedTokenizer) { entry.settle(epoch, held, entry.counter) })
		return
	}
	var counter *Counter
	pinMoved := err == nil
	if pinMoved {
		counter, err = l.load(ctx, pinned)
	}
	switch {
	case err == nil:
		logging.Info("prompt tokenizer loaded", logkey.Model, model, logkey.Tokenizer, pinned.String(), logkey.DurationMS, l.now().Sub(started).Milliseconds())
	case errors.Is(err, errParsePanicked):
		logging.Error("prompt tokenizer parse panicked; the model's prompts are not checked until its pin moves", logkey.Model, model, logkey.Tokenizer, pinned.String(), logkey.Error, err)
	case errors.Is(err, errNoTokenizer), errors.Is(err, ErrUnsupported):
		logging.Info("model has no tokenizer the gateway reads; its prompts are not checked against its context length", logkey.Model, model, logkey.Tokenizer, pinned.String(), logkey.Error, err)
	default:
		message := "prompt tokenizer could not be loaded; prompts are not checked until it is"
		if !pinMoved {
			message = "prompt tokenizer pin could not be read; a tokenizer already loaded keeps counting until it is"
		}
		logging.Warn(message, logkey.Model, model, logkey.Error, err)
		l.update(model, func(entry *pinnedTokenizer) { entry.retryAfter(l.now().Add(retryInterval), pinMoved) })
		return
	}
	l.update(model, func(entry *pinnedTokenizer) { entry.settle(epoch, pinned, counter) })
}

func (l *Library) readPin(ctx context.Context, model string, epoch uint64) (revision, error) {
	repo, commit, err := l.source.GetModelSource(ctx, epoch, model)
	pinned := revision{repo: repo, commit: commit}
	if err == nil && (repo == unpinned || commit == unpinned) {
		err = errNoTokenizer
	}
	return pinned, err
}

func (l *Library) update(model string, change func(entry *pinnedTokenizer)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	change(l.models[model])
}

func (l *Library) load(ctx context.Context, pinned revision) (*Counter, error) {
	address, err := url.JoinPath(huggingFaceBaseURL, pinned.repo, hubResolveSegment, pinned.commit, tokenizerFileName)
	if err != nil {
		return nil, fmt.Errorf("tokenizer url: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("tokenizer request: %w", err)
	}
	response, err := l.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", address, err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("fetch %s: %w", address, errNoTokenizer)
	case response.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("fetch %s: status %d", address, response.StatusCode)
	case response.ContentLength > maxTokenizerBytes:
		return nil, fmt.Errorf("%w: %s is %d bytes", ErrUnsupported, address, response.ContentLength)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxTokenizerBytes+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", address, err)
	case len(raw) > maxTokenizerBytes:
		return nil, fmt.Errorf("%w: %s is over %d bytes", ErrUnsupported, address, maxTokenizerBytes)
	}
	counter, err := l.parseRecovering(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: load %s: %w", ErrUnsupported, address, err)
	}
	return counter, nil
}

func (l *Library) parseRecovering(raw []byte) (counter *Counter, err error) {
	defer func() {
		if panicked := recover(); panicked != nil {
			counter, err = nil, fmt.Errorf("%w: %v", errParsePanicked, panicked)
		}
	}()
	return l.parse(bytes.NewReader(raw))
}
