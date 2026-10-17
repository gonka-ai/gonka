package params

import (
	"fmt"
	"net/url"
	"strings"
)

// MLNode is one mock (or real) ML endpoint in the AcquireMLNode pool (T7).
type MLNode struct {
	ID            string
	Endpoint      string
	MaxConcurrent int // 0 means unlimited
}

// ParseMLNodesEnv parses MOCK_ML_NODES: "id=url,id=url".
func ParseMLNodesEnv(raw string) ([]MLNode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]MLNode, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, endpoint, ok := strings.Cut(part, "=")
		id = strings.TrimSpace(id)
		endpoint = strings.TrimSpace(endpoint)
		if !ok || id == "" || endpoint == "" {
			return nil, fmt.Errorf("invalid MOCK_ML_NODES entry %q (want id=url)", part)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("duplicate MOCK_ML_NODES id %q", id)
		}
		seen[id] = struct{}{}
		if _, err := url.ParseRequestURI(endpoint); err != nil {
			return nil, fmt.Errorf("MOCK_ML_NODES id %q: bad endpoint %q: %w", id, endpoint, err)
		}
		out = append(out, MLNode{ID: id, Endpoint: endpoint})
	}
	return out, nil
}

// MLNodeFromEndpoint builds a single-node pool entry from MOCK_ML_ENDPOINT.
// Node id is the URL hostname (e.g. mock-openai / mock-openai-0).
func MLNodeFromEndpoint(endpoint string) (MLNode, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return MLNode{}, fmt.Errorf("empty ML endpoint")
	}
	u, err := url.ParseRequestURI(endpoint)
	if err != nil {
		return MLNode{}, fmt.Errorf("bad ML endpoint %q: %w", endpoint, err)
	}
	id := u.Hostname()
	if id == "" {
		id = "mock-openai"
	}
	return MLNode{ID: id, Endpoint: endpoint}, nil
}
