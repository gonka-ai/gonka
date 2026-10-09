package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

type refusalPeer struct {
	calls   int
	receipt []byte
	err     error
}

type noHistoryStore struct {
	storage.Storage
	calls int
}

func (s *noHistoryStore) GetDiffs(string, uint64, uint64) ([]types.DiffRecord, error) {
	s.calls++
	return nil, errors.New("history must not be loaded")
}

func TestRefusalHTTPProofAndOutcomes(t *testing.T) {
	for _, name := range []string{"receipt", "no_receipt", "unavailable", "bad_proof", "no_proof", "missing_target", "bad_payload", "conflicting_state"} {
		t.Run(name, func(t *testing.T) {
			env := setupServerEnv(t)
			seq, err := state.NewStateMachine("escrow-1", env.config, env.group, 100000, env.userSigner.Address(), signing.NewSecp256k1Verifier(), env.store)
			require.NoError(t, err)
			txs := []*types.DevshardTx{testutil.StartTx(1)}
			root, err := seq.ApplyLocal(1, txs)
			require.NoError(t, err)
			diff := testutil.SignDiffWithRoot(t, env.userSigner, "escrow-1", 1, txs, root)
			env.server.host.ApplyCatchUpDiffs([]types.Diff{diff})
			st := seq.ExportState()
			st.SealedAcc = make([]byte, 32)
			data, err := types.MarshalStateSnapshotProto(st, nil, nil)
			require.NoError(t, err)
			content, err := proto.Marshal(&types.StateSignatureContent{EscrowId: "escrow-1", Nonce: 1, StateRoot: root})
			require.NoError(t, err)
			sig, err := env.hostSigner.Sign(content)
			require.NoError(t, err)
			p := &types.RefusalPackage{EscrowID: "escrow-1", Version: st.StateRootAndProtocolVersion, N: 1, T: 1, Snapshot: data, Signatures: map[uint64]map[uint32][]byte{1: {0: sig}}}
			peer := &refusalPeer{}
			peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request ChallengeReceiptRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Refusal == nil {
					w.WriteHeader(400)
					return
				}
				peer.calls++
				if peer.err != nil {
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{Receipt: peer.receipt})
			}))
			defer peerServer.Close()
			env.server.peerClients = map[int]*HTTPClient{0: NewHTTPClient(peerServer.URL, "escrow-1", env.hostSigner)}
			guarded := &noHistoryStore{Storage: env.store}
			env.server.store = guarded
			req := VerifyTimeoutRequest{InferenceID: 1, Reason: "refused", Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}}
			switch name {
			case "receipt":
				peer.receipt = []byte("receipt")
			case "unavailable":
				peer.err = errors.New("unavailable")
			case "bad_proof":
				p.Signatures[1][0] = []byte("bad")
			case "conflicting_state":
				st.Balance++
				p.Snapshot, err = types.MarshalStateSnapshotProto(st, nil, nil)
				require.NoError(t, err)
				root, err = state.SnapshotRoot(st)
				require.NoError(t, err)
				content, err = proto.Marshal(&types.StateSignatureContent{EscrowId: "escrow-1", Nonce: 1, StateRoot: root})
				require.NoError(t, err)
				p.Signatures[1][0], err = env.hostSigner.Sign(content)
				require.NoError(t, err)
			case "no_proof":
				p = nil
			case "bad_payload":
				req.Payload.Model = "wrong"
			case "missing_target":
				req.InferenceID = 2 // missing local target rejects before proof replay
			}
			req.Refusal, err = refusalToJSON(p)
			require.NoError(t, err)
			body, err := json.Marshal(req)
			require.NoError(t, err)
			result := env.doPost(t, testRoutePrefix+"/sessions/escrow-1/verify-timeout", body)
			require.Zero(t, guarded.calls)
			if name == "bad_proof" || name == "no_proof" || name == "missing_target" || name == "conflicting_state" {
				require.NotEqual(t, http.StatusOK, result.Code)
				require.Zero(t, peer.calls)
				return
			}
			require.Equal(t, http.StatusOK, result.Code, result.Body.String())
			var resp VerifyTimeoutResponse
			require.NoError(t, json.Unmarshal(result.Body.Bytes(), &resp))
			require.Equal(t, name == "no_receipt" || name == "unavailable", resp.Accept)
			if name == "bad_payload" {
				require.Zero(t, peer.calls)
			} else {
				require.Equal(t, 1, peer.calls)
			}
		})
	}
}

func TestRefusalWireRoundTrip(t *testing.T) {
	p := &types.RefusalPackage{EscrowID: "escrow-1", N: 0, T: 1, Snapshot: []byte("opaque"), Diffs: []types.Diff{{Nonce: 1, Txs: []*types.DevshardTx{testutil.StartTx(1)}, PostStateRoot: make([]byte, 32)}}}
	wire, err := refusalToJSON(p)
	require.NoError(t, err)
	data, err := json.Marshal(wire)
	require.NoError(t, err)
	var decoded RefusalPackageJSON
	require.NoError(t, json.Unmarshal(data, &decoded))
	got, err := refusalFromJSON(&decoded)
	require.NoError(t, err)
	require.True(t, proto.Equal(p.Diffs[0].Txs[0], got.Diffs[0].Txs[0]))
	require.Equal(t, p.Snapshot, got.Snapshot)
}

func TestRefusalHTTPExecutorImportsSnapshot(t *testing.T) {
	env := setupServerEnv(t)
	seq, err := state.NewStateMachine("escrow-1", env.config, env.group, 100000, env.userSigner.Address(), signing.NewSecp256k1Verifier(), env.store)
	require.NoError(t, err)
	root, err := seq.ApplyLocal(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.NoError(t, err)
	st := seq.ExportState()
	st.SealedAcc = make([]byte, 32)
	data, err := types.MarshalStateSnapshotProto(st, nil, nil)
	require.NoError(t, err)
	content, _ := proto.Marshal(&types.StateSignatureContent{EscrowId: "escrow-1", Nonce: 1, StateRoot: root})
	sig, err := env.hostSigner.Sign(content)
	require.NoError(t, err)
	req := ChallengeReceiptRequest{InferenceID: 1, Payload: &PayloadJSON{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}, Refusal: &RefusalPackageJSON{EscrowID: "escrow-1", Version: st.StateRootAndProtocolVersion, N: 1, T: 1, Snapshot: data, Signatures: map[uint64]map[uint32][]byte{1: {0: sig}}}}
	body, err := json.Marshal(req)
	require.NoError(t, err)
	response := env.doPostAs(t, testRoutePrefix+"/sessions/escrow-1/challenge-receipt", body, env.hostSigner)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var receipt ChallengeReceiptResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &receipt))
	require.NotEmpty(t, receipt.Receipt)
	meta, err := env.store.GetSessionMeta("escrow-1")
	require.NoError(t, err)
	require.Equal(t, uint64(1), meta.ImportedNonce)
	rows, err := env.store.GetDiffs("escrow-1", 1, 1)
	require.NoError(t, err)
	require.Empty(t, rows)
}
