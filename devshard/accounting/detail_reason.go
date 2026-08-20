package accounting

import "strings"

// The host's work is real; only the delivery is not. Exported because devshardctl writes it.
const DeliveryClientGone = "client_gone_before_delivery"

// The gateway spent this nonce on itself, so it is real work by the host but not work for a user.
const DeliveryWarmupProbe = "warmup_probe"

func normalizeDetailReason(reason string) string {
	reason = strings.TrimSpace(reason)
	switch reason {
	case "", "none":
		return ""
	case "phase_transition_aborted", "error_stream", "empty_stream", "model_burn_empty", "sse_truncated",
		"eof_transport", "client_cancelled", "transport_error", "no_receipt",
		"not_finished", "http_429", "http_503", "http_forbidden", "http_not_found",
		"http_timestamp_drift", "http_error", "long_response_after_content",
		"escrow_state_root_diverged", "context_canceled", "timeout_diff_delivery_failed",
		"timeout_not_applied", "poc_unavailable_host", "participant_throttled_no_send",
		"participant_capability_no_send", "no_compatible_request_after_stale",
		DeliveryClientGone:
		return reason
	default:
		return "unknown"
	}
}

// normalizeDeliveryReason keeps what can describe a delivery. It is narrower than the detail
// vocabulary on purpose: a delivery cannot be "poc_unavailable_host" or "timeout_not_applied", and
// admitting those widens the counter key with combinations that never occur.
func normalizeDeliveryReason(reason string) string {
	reason = strings.TrimSpace(reason)
	switch reason {
	case "", "none":
		return ""
	case "empty_stream", "model_burn_empty", "error_stream", "sse_truncated",
		"eof_transport", "transport_error", "client_cancelled", "not_finished",
		"no_receipt", "http_error", "http_429", "http_503", "http_not_found",
		"http_forbidden", DeliveryClientGone, DeliveryWarmupProbe:
		return reason
	default:
		return "unknown"
	}
}
