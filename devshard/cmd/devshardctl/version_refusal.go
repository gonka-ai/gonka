package main

import (
	"strings"

	"devshard/accounting"
)

const (
	versionRefusalSubject = `version "`
	versionRefusalVerdict = `" not found`
)

func isVersionRefusal(body string) bool {
	return strings.Contains(body, versionRefusalSubject) && strings.Contains(body, versionRefusalVerdict)
}

func deliveryReasonFor(inf *inflight, session nonceFinishedChecker, winnerNonce uint64, ok bool, clientGone *cancelFlag) string {
	switch {
	case !ok:
		return gatewayAttemptFailureReason(inf, session, "")
	case inf != nil && inf.nonce == winnerNonce && clientGone.Gone():
		return accounting.DeliveryClientGone
	}
	return ""
}
