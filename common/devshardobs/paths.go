package devshardobs

import "strings"

// IsVersionlessObsPath reports whether path (with or without a leading
// /devshard/ prefix) is a public versionless observability path.
// Gateway /v1/debug/* is intentionally excluded.
func IsVersionlessObsPath(path string) bool {
	path = normalizeObsPath(path)
	switch path {
	case "metrics":
		return true
	}
	if path == "stats/shards" || strings.HasPrefix(path, "stats/shards/") {
		return true
	}
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[0] == "sessions" {
		switch parts[2] {
		case "diffs", "mempool", "signatures":
			return true
		}
	}
	return false
}

// EscrowIDFromObsPath extracts the escrow id from session-scoped or
// stats-detail versionless paths. ok=false for process-level paths
// (metrics, /stats/shards list).
func EscrowIDFromObsPath(path string) (string, bool) {
	path = normalizeObsPath(path)
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[0] == "sessions" {
		switch parts[2] {
		case "diffs", "mempool", "signatures":
			if parts[1] != "" {
				return parts[1], true
			}
		}
	}
	if len(parts) == 3 && parts[0] == "stats" && parts[1] == "shards" && parts[2] != "" {
		return parts[2], true
	}
	return "", false
}

// normalizeObsPath strips a leading slash and optional "devshard/" prefix.
func normalizeObsPath(path string) string {
	path = strings.TrimPrefix(path, "/")
	return strings.TrimPrefix(path, "devshard/")
}

// obsRestPath returns the path after /devshard (e.g. "/sessions/42/diffs").
func obsRestPath(path string) string {
	return "/" + normalizeObsPath(path)
}
