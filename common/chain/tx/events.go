package tx

import (
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	abcitypes "github.com/cometbft/cometbft/abci/types"
)

const devshardEscrowCreatedEvent = "devshard_escrow_created"

// CreatedEscrowIDFromTxResponse extracts escrow_id from tx events.
func CreatedEscrowIDFromTxResponse(resp *sdk.TxResponse) (uint64, bool) {
	if resp == nil {
		return 0, false
	}
	if id, ok := createdEscrowIDFromABCIEvents(resp.Events); ok {
		return id, true
	}
	for _, log := range resp.Logs {
		if id, ok := createdEscrowIDFromStringEvents(log.Events); ok {
			return id, true
		}
	}
	return 0, false
}

func createdEscrowIDFromABCIEvents(events []abcitypes.Event) (uint64, bool) {
	for _, event := range events {
		if event.Type != devshardEscrowCreatedEvent {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key != "escrow_id" {
				continue
			}
			id, err := strconv.ParseUint(attr.Value, 10, 64)
			if err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}

func createdEscrowIDFromStringEvents(events sdk.StringEvents) (uint64, bool) {
	for _, event := range events {
		if event.Type != devshardEscrowCreatedEvent {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key != "escrow_id" {
				continue
			}
			id, err := strconv.ParseUint(attr.Value, 10, 64)
			if err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}
