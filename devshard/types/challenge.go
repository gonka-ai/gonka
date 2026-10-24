package types

import (
	"strconv"
	"strings"
	"time"
)

// ChallengeProtocolVersion is the first state-root protocol whose hosts know
// MsgChallenge and VerifyChallenge.
const ChallengeProtocolVersion = "v7"

// ProtocolKnowsChallenge reports whether a session on the given state-root
// protocol version can carry MsgChallenge: a numeric version at or past
// ChallengeProtocolVersion, or this binary's own named stamp (testenv
// builds stamp "dev" on every binary). A session before v7 never carries the
// tx, and a session on v7 stays challengeable after the binary moves on.
func ProtocolKnowsChallenge(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	if version == EffectiveStateRootAndProtocolVersion {
		return true
	}
	have, ok := numericProtocolVersion(version)
	if !ok {
		return false
	}
	want, _ := numericProtocolVersion(ChallengeProtocolVersion)
	return have >= want
}

// numericProtocolVersion is the major number of a "vN" or "vN.x" stamp; a
// named stamp has none.
func numericProtocolVersion(version string) (int, bool) {
	parsed, err := ParseProtocolVersion(version)
	if err != nil {
		return 0, false
	}
	major, _, _ := strings.Cut(string(parsed), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0, false
	}
	return n, true
}

// ChallengeWindow is the hosts' fetch window for a session with the given
// seal grace: ChallengeFetchWindowSeconds, capped at half the grace so the
// votes reach a diff before the record seals. The hosts and the gateway use
// the same cap.
func ChallengeWindow(sealGraceSeconds uint32) time.Duration {
	window := time.Duration(ChallengeFetchWindowSeconds) * time.Second
	if grace := time.Duration(sealGraceSeconds) * time.Second; grace > 0 && window > grace/2 {
		window = grace / 2
	}
	return window
}

// ChallengeReasonLabel is the metric and log label of a challenge reason.
func ChallengeReasonLabel(reason ChallengeReason) string {
	switch reason {
	case ChallengeReason_CHALLENGE_REASON_STREAM_UNBOUND:
		return "stream_unbound"
	case ChallengeReason_CHALLENGE_REASON_EMPTY_STREAM:
		return "empty_stream"
	default:
		return "unspecified"
	}
}

// ChallengeFaultLabel is the metric and log label of a vote's fault.
func ChallengeFaultLabel(fault ChallengeFault) string {
	switch fault {
	case ChallengeFault_CHALLENGE_FAULT_NONE:
		return "none"
	case ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE:
		return "payload_unavailable"
	case ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH:
		return "hash_mismatch"
	case ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM:
		return "empty_stream"
	case ChallengeFault_CHALLENGE_FAULT_USAGE_INFLATED:
		return "usage_inflated"
	default:
		return "unknown"
	}
}

// KimiK26ModelID is the reasoning model whose answer can come back with no
// content positions while it reports completion tokens: the model spent its
// budget inside a reasoning block that vLLM strips. The gateway does not
// count such a stream as empty, and a voter does not count the stored body
// as empty either, so an honest Kimi answer is not challenged for it.
const KimiK26ModelID = "moonshotai/Kimi-K2.6"

// ModelBurnsEmpty reports a model whose empty content with reported
// completion tokens is a documented outcome, not a fault.
func ModelBurnsEmpty(model string) bool {
	return model == KimiK26ModelID
}
