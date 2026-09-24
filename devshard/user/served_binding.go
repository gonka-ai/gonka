package user

import (
	"bytes"

	"devshard/host"
	"devshard/types"
)

type ServedBinding string

const (
	ServedBindingNoFinish         ServedBinding = "no_finish"
	ServedBindingNothingReceived  ServedBinding = "nothing_received"
	ServedBindingUnverifiedFinish ServedBinding = "unverified_finish"
	ServedBindingBound            ServedBinding = "bound"
	ServedBindingMismatch         ServedBinding = "mismatch"
	ServedBindingMissing          ServedBinding = "missing"
)

func CheckServedBinding(response *host.HostResponse, nonce uint64, rejectUnverified func(*types.DevshardTx) error) ServedBinding {
	if response == nil {
		return ServedBindingNoFinish
	}
	finish, sawUnverified := acceptedFinishFor(response.Mempool, nonce, rejectUnverified)
	if finish == nil && sawUnverified {
		return ServedBindingUnverifiedFinish
	}
	if finish == nil {
		return ServedBindingNoFinish
	}
	if len(response.ReceivedResponseHashes) == 0 {
		return ServedBindingNothingReceived
	}
	for _, received := range response.ReceivedResponseHashes {
		if bytes.Equal(received[:], finish.ResponseHash) || bytes.Equal(received[:], finish.ServedHash) {
			return ServedBindingBound
		}
	}
	if len(finish.ServedHash) == 0 {
		return ServedBindingMissing
	}
	return ServedBindingMismatch
}

func (s *Session) CheckServedBinding(response *host.HostResponse, nonce uint64) ServedBinding {
	return CheckServedBinding(response, nonce, s.rejectUnverifiedHostTx)
}

func acceptedFinishFor(txs []*types.DevshardTx, nonce uint64, rejectUnverified func(*types.DevshardTx) error) (accepted *types.MsgFinishInference, sawUnverified bool) {
	for _, tx := range txs {
		finish := tx.GetFinishInference()
		if finish == nil || finish.InferenceId != nonce {
			continue
		}
		if rejectUnverified(tx) != nil {
			sawUnverified = true
			continue
		}
		return finish, sawUnverified
	}
	return nil, sawUnverified
}
