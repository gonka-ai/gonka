package bls

import (
	"errors"

	"decentralized-api/cosmosclient/tx_manager"
)

func isQueuedForRetry(err error) bool {
	return errors.Is(err, tx_manager.ErrTxFailedToBroadcastAndPutOnRetry)
}
