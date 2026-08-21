package main

import (
	"regexp"
	"strings"

	"devshard/accounting"
	"devshard/storage"
)

var versionNotFound = regexp.MustCompile(`version "[^"]*" not found`)

func isVersionRefusal(body string) bool {
	return versionNotFound.MatchString(body) || strings.Contains(body, storage.ErrSessionVersionConflict.Error())
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
