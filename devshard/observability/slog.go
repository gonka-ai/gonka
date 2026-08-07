package observability

import (
	commonobs "common/observability"
)

// InstallLogger installs the shared TraceHandler as the process default slog
// handler. format is "json" or "text" (default); see common/observability.
// request_id stamping is registered once in common/observability.
func InstallLogger(format string) {
	commonobs.InstallLogger(format)
}
