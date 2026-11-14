//go:build devshard_testenv

package transport

import (
	"os"
	"path/filepath"
	"time"

	"devshard/host"
	"devshard/logging"
	"devshard/types"
)

// Executor faults for testenv timeout scenarios, each switched by a trip file
// so a citest can flip it with docker exec while the stack runs. Production
// builds compile executor_fault_prod.go instead and never read these files.
const (
	envTestenvExecutorFaultDir = "DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR"
	defaultExecutorFaultDir    = "/tmp"

	// ExecutorFaultDropPayloadFile makes ServeInference discard the inference
	// payload: diffs still apply and the state is signed, but no receipt is
	// signed and nothing runs, so the user sees a refusal.
	ExecutorFaultDropPayloadFile = "devshard-fault-drop-payload"
	// ExecutorFaultForgeChallengeFile makes ServeChallengeReceipt answer with
	// evidence that names the inference, escrow, and executor slot but carries
	// no valid executor signature.
	ExecutorFaultForgeChallengeFile = "devshard-fault-forge-challenge"
)

// ForgedChallengeSig is the receipt and proposer signature a forged
// challenge answer carries.
var ForgedChallengeSig = []byte("testenv-forged-executor-sig")

func executorFaultActive(name string) bool {
	dir := os.Getenv(envTestenvExecutorFaultDir)
	if dir == "" {
		dir = defaultExecutorFaultDir
	}
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func executorDropsPayload() bool {
	if !executorFaultActive(ExecutorFaultDropPayloadFile) {
		return false
	}
	logging.Warn("testenv executor fault: dropping inference payload", "subsystem", "transport")
	return true
}

func forgedChallengeReceipt(h *host.Host, inferenceID uint64) (*ChallengeReceiptResponse, bool) {
	if !executorFaultActive(ExecutorFaultForgeChallengeFile) {
		return nil, false
	}
	group := h.Group()
	if len(group) == 0 {
		return nil, false
	}
	mempool, err := DevshardTxsToBytes([]*types.DevshardTx{
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: inferenceID,
			ExecutorSig: ForgedChallengeSig,
			ConfirmedAt: time.Now().Unix(),
		}}},
		{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
			InferenceId:  inferenceID,
			EscrowId:     h.EscrowID(),
			ExecutorSlot: group[inferenceID%uint64(len(group))].SlotID,
			ProposerSig:  ForgedChallengeSig,
		}}},
	})
	if err != nil {
		return nil, false
	}
	logging.Warn("testenv executor fault: forging challenge-receipt answer",
		"subsystem", "transport", "inference_id", inferenceID)
	return &ChallengeReceiptResponse{Receipt: ForgedChallengeSig, Mempool: mempool}, true
}
