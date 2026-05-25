package teeverify

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type Verifier interface {
	Provider() string

	Verify(ctx context.Context, req VerifyRequest) (*Report, error)
}

type VerifyRequest struct {
	PublicKey []byte

	Quote []byte

	EnvelopeVersion string
}

type Report struct {
	Measurement string

	TcbStatus string

	QuoteHash []byte
}

type Registry struct {
	mu        sync.RWMutex
	verifiers map[string]Verifier
}

func NewRegistry() *Registry {
	return &Registry{verifiers: make(map[string]Verifier)}
}

func (r *Registry) Register(v Verifier) {
	if v == nil {
		panic("teeverify: Register called with nil Verifier")
	}
	name := v.Provider()
	if name == "" {
		panic("teeverify: Verifier.Provider() returned an empty string")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.verifiers[name]; exists {
		panic(fmt.Sprintf("teeverify: duplicate Verifier registered for provider %q", name))
	}
	r.verifiers[name] = v
}

func (r *Registry) Lookup(provider string) (Verifier, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.verifiers[provider]
	return v, ok
}

func (r *Registry) Providers() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.verifiers))
	for k := range r.verifiers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
