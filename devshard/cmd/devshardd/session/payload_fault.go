package session

import (
	"strconv"
	"strings"
)

// Payload withholding fault injection for testenv scenarios. The reader of
// these env vars is compiled in only under the devshard_testenv build tag
// (payload_fault_testenv.go); production builds get the stub in
// payload_fault_prod.go, so a stray env var cannot turn a real executor into a
// payload withholder.
const (
	envTestenvPayloadHTTPStatus = "DEVSHARD_TESTENV_PAYLOAD_HTTP_STATUS"
	envTestenvPayloadFaultAddr  = "DEVSHARD_TESTENV_PAYLOAD_FAULT_VALIDATOR"
)

// parsePayloadFault validates the raw env values. A non-numeric or non-positive
// status disables injection.
func parsePayloadFault(statusRaw, addrRaw string) (status int, validatorAddr string) {
	raw := strings.TrimSpace(statusRaw)
	if raw == "" {
		return 0, ""
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, ""
	}
	return n, strings.TrimSpace(addrRaw)
}

// payloadFaultMatches reports whether this payload GET should be failed for
// the given validator address. An empty onlyAddr fails every caller.
func payloadFaultMatches(status int, onlyAddr, validatorAddr string) bool {
	if status <= 0 {
		return false
	}
	if onlyAddr != "" && onlyAddr != validatorAddr {
		return false
	}
	return true
}
