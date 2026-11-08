package accounting

import "devshard/cmd/gateway/engine"

// Names only the dispositions a timeout can be owed for; the caller has already found the round unsettled.
func awaitingTimeout(key CounterKey) bool {
	return isUnfinishedDisposition(key.Disposition)
}

// The ledger's check on itself. See README.md, "The ledger's check on itself".
func namesNoReason(key CounterKey) bool {
	return key.Terminal == engine.TerminalNameUnnamed ||
		key.Terminal == engine.TerminalNameUnclassified ||
		(key.Disposition == DispositionGhost && key.GhostReason == "")
}
