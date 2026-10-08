package state

import (
	"fmt"
	"testing"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/types"
)

const benchApplyHosts = 5

// BenchmarkApplyDiff runs one MsgStartInference diff through each apply entry
// point against n live Finished records and n sealed ids:
//
//   - apply: ApplyLocal, the compose and replay path (applyCore).
//   - preview-commit: PreviewLocalBestEffort then CommitValidated, the user
//     persist-first path.
//   - validate-commit: ValidateDiff of a user-signed diff then
//     CommitValidated, the host persist-first path. The user root and the
//     signature are prepared with the timer stopped.
//
// Each iteration adds one Pending record, so the live set grows by b.N.
func BenchmarkApplyDiff(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 30 * 60 * 32 / 2} {
		b.Run(fmt.Sprintf("apply/n=%d", n), func(b *testing.B) {
			env := newBenchApplyEnv(b)
			sm := env.machine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nonce := sm.LatestNonce() + 1
				if _, err := sm.ApplyLocal(nonce, []*types.DevshardTx{startTx(nonce)}); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("preview-commit/n=%d", n), func(b *testing.B) {
			env := newBenchApplyEnv(b)
			sm := env.machine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nonce := sm.LatestNonce() + 1
				vd, err := sm.PreviewLocalBestEffort(nonce, []*types.DevshardTx{startTx(nonce)})
				if err != nil {
					b.Fatal(err)
				}
				if !sm.CommitValidated(vd) {
					b.Fatal("commit rejected")
				}
			}
		})
		b.Run(fmt.Sprintf("validate-commit/n=%d", n), func(b *testing.B) {
			env := newBenchApplyEnv(b)
			user := env.machine(b, n)
			host := env.machine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				nonce := user.LatestNonce() + 1
				txs := []*types.DevshardTx{startTx(nonce)}
				root, err := user.ApplyLocal(nonce, txs)
				if err != nil {
					b.Fatal(err)
				}
				diff := env.signDiff(b, nonce, txs, root)
				b.StartTimer()
				vd, err := host.ValidateDiff(diff)
				if err != nil {
					b.Fatal(err)
				}
				if !host.CommitValidated(vd) {
					b.Fatal("commit rejected")
				}
			}
		})
	}
}

type benchApplyEnv struct {
	user  *signing.Secp256k1Signer
	group []types.SlotAssignment
}

func newBenchApplyEnv(b *testing.B) benchApplyEnv {
	b.Helper()
	hosts := make([]*signing.Secp256k1Signer, benchApplyHosts)
	for i := range hosts {
		hosts[i] = benchKey(b)
	}
	return benchApplyEnv{user: benchKey(b), group: testutil.MakeGroup(hosts)}
}

func benchKey(b *testing.B) *signing.Secp256k1Signer {
	b.Helper()
	key, err := signing.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	return key
}

// machine seeds n live Finished records (ids 1..n) and n sealed ids
// (n+1..2n). The records share one ConfirmedAt, so auto-seal scans them on
// its nonces and seals none.
func (e benchApplyEnv) machine(b *testing.B, n int) *StateMachine {
	b.Helper()
	sm, err := NewStateMachine("escrow-bench", testutil.DefaultConfig(benchApplyHosts), e.group,
		1<<50, e.user.Address(), signing.NewSecp256k1Verifier(), storage.NewMemory())
	if err != nil {
		b.Fatal(err)
	}
	inferences, entries := benchFinishedSet(b, n)
	sm.state.Inferences = inferences
	sm.committedEntries = entries
	sm.liveEntryXOR = xorInferencesHashFromEntries(entries)
	for id := uint64(n + 1); id <= uint64(2*n); id++ {
		sm.sealedNonces[id] = id
	}
	sm.state.LatestNonce = uint64(2 * n)
	return sm
}

func (e benchApplyEnv) signDiff(b *testing.B, nonce uint64, txs []*types.DevshardTx, root []byte) types.Diff {
	b.Helper()
	content := BuildDiffContent("escrow-bench", nonce, txs, root)
	data, err := deterministicMarshal.Marshal(content)
	if err != nil {
		b.Fatal(err)
	}
	sig, err := e.user.Sign(data)
	if err != nil {
		b.Fatal(err)
	}
	return types.Diff{Nonce: nonce, Txs: txs, UserSig: sig, PostStateRoot: root}
}
