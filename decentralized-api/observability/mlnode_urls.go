package observability

import "net/url"

// MLNodeMetricsPath is the federation scrape path under PoCUrl().
const MLNodeMetricsPath = "/api/v1/metrics"

// MLNodeClockPath is the preferred clock path under PoCUrl() (Step 5 on mlnode).
const MLNodeClockPath = "/api/v1/clock"

// MLNodeReadyzPath is the root-mounted fallback for the dapi ping job.
// Never /health or /livez — those share a heavy cached handler.
const MLNodeReadyzPath = "/readyz"

// JoinMLNodePoCPath joins an unversioned PoCUrl() base with a path segment,
// matching federation's url.JoinPath(n.Node.PoCUrl(), …) construction.
func JoinMLNodePoCPath(pocURL, path string) (string, error) {
	return url.JoinPath(pocURL, path)
}
