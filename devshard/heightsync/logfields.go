package heightsync

// Structured log keys for height-sync debug lines (Loki contract).
// Keep in sync with CONTAINER_E2E_PLAN.md §6 and Alloy parsers.
const (
	LogFieldSubsystem           = "subsystem"
	LogFieldDirection           = "direction"
	LogFieldMode                = "mode"
	LogFieldNonce               = "nonce"
	LogFieldPeerID              = "peer_id"
	LogFieldHostID              = "host_id"
	LogFieldSessionID           = "session_id"
	LogFieldBlockHashPrefix     = "block_hash_prefix"
	LogFieldPeerBlockHashPrefix = "peer_block_hash_prefix"
	LogFieldPeerHeight          = "peer_height"
	LogFieldHeight              = "height"
	LogFieldLocalAligned        = "local_aligned"
	LogFieldDelta               = "delta"
	LogFieldTrustLevel          = "trust_level"
	LogFieldForcedStart         = "forced_start"
	LogFieldForcedEnd           = "forced_end"
	LogFieldSource              = "source"
	LogFieldTag                 = "tag"
	LogFieldReason              = "reason"
	LogFieldCheck               = "check"
	LogFieldVerdict             = "verdict"
	LogFieldClassification      = "classification"
	LogFieldCacheReady          = "cache_ready"
	LogFieldVerifiedOrigins     = "verified_origins"
	LogFieldEvent               = "event"
)
