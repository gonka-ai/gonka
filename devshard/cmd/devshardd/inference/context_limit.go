package inference

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"devshard/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ModelArgsSource returns the vLLM arguments the chain pins for an (epoch, model).
type ModelArgsSource interface {
	GetModelArgs(ctx context.Context, epochID uint64, modelID string) ([]string, error)
}

// ContextLimitResolver returns the governance --max-model-len of an (epoch, model), or 0 when it is unknown.
type ContextLimitResolver interface {
	Resolve(ctx context.Context, epochID uint64, model string) uint64
}

type contextLimitKey struct {
	epochID uint64
	model   string
}

// ChainContextLimits reads --max-model-len from the epoch's model snapshot. A snapshot never changes
// within its epoch, so a found limit is cached; a failure is asked again next time.
type ChainContextLimits struct {
	source ModelArgsSource

	mu    sync.Mutex
	cache map[contextLimitKey]uint64
}

func NewChainContextLimits(source ModelArgsSource) *ChainContextLimits {
	return &ChainContextLimits{source: source, cache: make(map[contextLimitKey]uint64)}
}

func (r *ChainContextLimits) Resolve(ctx context.Context, epochID uint64, model string) uint64 {
	if r == nil || r.source == nil {
		return 0
	}
	key := contextLimitKey{epochID: epochID, model: model}
	r.mu.Lock()
	limit, cached := r.cache[key]
	r.mu.Unlock()
	if cached {
		return limit
	}
	args, err := r.source.GetModelArgs(ctx, epochID, model)
	if err != nil {
		logging.Warn("model context limit unknown", types.Validation, "epoch", epochID, "model", model, "error", err)
		return 0
	}
	limit = MaxModelLen(args)
	r.mu.Lock()
	r.cache[key] = limit
	r.mu.Unlock()
	return limit
}

// MaxModelLen returns the --max-model-len in vLLM args, the last one when repeated as vLLM reads it, or 0.
func MaxModelLen(args []string) uint64 {
	var limit uint64
	for i, arg := range args {
		value, found := "", false
		if v, ok := strings.CutPrefix(arg, "--max-model-len="); ok {
			value, found = v, true
		} else if arg == "--max-model-len" && i+1 < len(args) {
			value, found = args[i+1], true
		}
		if !found {
			continue
		}
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			limit = 0
			continue
		}
		limit = parsed
	}
	return limit
}
