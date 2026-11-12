package escrow

import "devshard/cmd/gateway/store"

func goneFromChain(record store.DevshardRecord) bool {
	return record.GoneFromChain && !record.Active
}

func unsettledCount(modelID string, devshards []store.DevshardRecord, commitments []store.Commitment) int {
	counted := 0
	for _, record := range devshards {
		if record.Model == modelID && !goneFromChain(record) {
			counted++
		}
	}
	for _, commitment := range commitments {
		if commitment.Model == modelID {
			counted++
		}
	}
	return counted
}
