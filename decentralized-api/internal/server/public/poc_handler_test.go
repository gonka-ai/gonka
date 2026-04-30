package public

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"decentralized-api/cosmosclient"
	"decentralized-api/internal/authzcache"
	"decentralized-api/poc/artifacts"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type proofQueryServer struct {
	types.UnimplementedQueryServer
	pubkeyB64 string
}

func (s *proofQueryServer) GranteesByMessageType(context.Context, *types.QueryGranteesByMessageTypeRequest) (*types.QueryGranteesByMessageTypeResponse, error) {
	return &types.QueryGranteesByMessageTypeResponse{}, nil
}

func (s *proofQueryServer) AccountByAddress(_ context.Context, req *types.QueryAccountByAddressRequest) (*types.QueryAccountByAddressResponse, error) {
	return &types.QueryAccountByAddressResponse{Pubkey: s.pubkeyB64}, nil
}

func newProofQueryClient(t *testing.T, server *proofQueryServer) (types.QueryClient, func()) {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	types.RegisterQueryServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("grpc dial failed: %v", err)
	}

	return types.NewQueryClient(conn), func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	}
}

func TestBuildPocProofsSignPayload(t *testing.T) {
	rootHash := make([]byte, 32)
	for i := range rootHash {
		rootHash[i] = byte(i)
	}

	req := &PocProofsRequest{
		PocStageStartBlockHeight: 12345,
		ModelId:                  "model-a",
		RootHash:                 base64.StdEncoding.EncodeToString(rootHash),
		Count:                    50000,
		LeafIndices:              []StringUint32{0, 42, 999},
		ValidatorAddress:         "gonka1validator",
		ValidatorSignerAddress:   "gonka1signer",
		Timestamp:                1700000000000000000,
	}

	payload := buildPocProofsSignPayload(req, rootHash)

	// Verify payload is 64 bytes (hex-encoded SHA256 hash)
	assert.Len(t, payload, 64)

	// Manually construct expected payload to verify format.
	// Variable-length string fields are length-prefixed (LE32) so distinct
	// semantic tuples cannot collide.
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, int64(12345))
	binary.Write(buf, binary.LittleEndian, uint32(len("model-a")))
	buf.WriteString("model-a")
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, uint32(50000))
	binary.Write(buf, binary.LittleEndian, uint32(3)) // num_leaf_indices
	binary.Write(buf, binary.LittleEndian, uint32(0))
	binary.Write(buf, binary.LittleEndian, uint32(42))
	binary.Write(buf, binary.LittleEndian, uint32(999))
	binary.Write(buf, binary.LittleEndian, int64(1700000000000000000))
	binary.Write(buf, binary.LittleEndian, uint32(len("gonka1validator")))
	buf.WriteString("gonka1validator")
	binary.Write(buf, binary.LittleEndian, uint32(len("gonka1signer")))
	buf.WriteString("gonka1signer")

	expectedHash := sha256.Sum256(buf.Bytes())
	expectedHex := fmt.Sprintf("%x", expectedHash)
	assert.Equal(t, expectedHex, string(payload))
}

func TestStringInt64_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
	}{
		{"number", `12345`, 12345},
		{"string", `"12345"`, 12345},
		{"large number", `1700000000000000000`, 1700000000000000000},
		{"large string", `"1700000000000000000"`, 1700000000000000000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s StringInt64
			err := json.Unmarshal([]byte(tc.input), &s)
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, int64(s))
		})
	}
}

func TestStringUint32_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected uint32
	}{
		{"number", `12345`, 12345},
		{"string", `"12345"`, 12345},
		{"zero", `0`, 0},
		{"zero string", `"0"`, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s StringUint32
			err := json.Unmarshal([]byte(tc.input), &s)
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, uint32(s))
		})
	}
}

func TestVerifyPocProofsSignatureWithPubkey_InvalidSignatureEncoding(t *testing.T) {
	rootHash := make([]byte, 32)
	req := &PocProofsRequest{
		Signature: "not-valid-base64!!!",
	}

	err := verifyPocProofsSignatureWithPubkey(req, rootHash, base64.StdEncoding.EncodeToString([]byte("pubkey")))
	assert.Error(t, err)
}

func TestVerifyPocProofsSignatureWithPubkey_InvalidPubkeyEncoding(t *testing.T) {
	rootHash := make([]byte, 32)
	req := &PocProofsRequest{
		Signature: base64.StdEncoding.EncodeToString([]byte("somesig")),
	}

	err := verifyPocProofsSignatureWithPubkey(req, rootHash, "not-valid-base64!!!")
	assert.Error(t, err)
}

func TestVerifyPocProofsSignatureWithPubkey_InvalidSignature(t *testing.T) {
	rootHash := make([]byte, 32)
	req := &PocProofsRequest{
		Signature: base64.StdEncoding.EncodeToString([]byte("somesig")),
	}

	// Valid base64 but invalid secp256k1 pubkey
	err := verifyPocProofsSignatureWithPubkey(req, rootHash, base64.StdEncoding.EncodeToString([]byte("pubkey")))
	assert.Error(t, err)
}

func TestPocProofsRequest_MaxLeafIndices(t *testing.T) {
	// Verify the constant is set correctly
	assert.Equal(t, 500, maxLeafIndicesPerRequest)
}

// TestBuildPocProofsSignPayload_KotlinCompatibility verifies the payload matches
// what Kotlin's buildPocProofsSignPayload produces with the same inputs.
func TestBuildPocProofsSignPayload_KotlinCompatibility(t *testing.T) {
	rootHash := make([]byte, 32)
	for i := range rootHash {
		rootHash[i] = byte(i)
	}

	req := &PocProofsRequest{
		PocStageStartBlockHeight: 45,
		ModelId:                  "model-a",
		RootHash:                 base64.StdEncoding.EncodeToString(rootHash),
		Count:                    100,
		LeafIndices:              []StringUint32{0, 1, 2},
		ValidatorAddress:         "gonka1test",
		ValidatorSignerAddress:   "gonka1test",
		Timestamp:                1768556941222626000,
	}

	payload := buildPocProofsSignPayload(req, rootHash)

	// Verify it's 64 bytes (hex string)
	assert.Len(t, payload, 64, "Payload should be 64 bytes (hex string)")

	// Verify the binary structure is correct by rebuilding manually with
	// length-prefixed variable fields. Must mirror the Kotlin signer.
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, int64(45))
	binary.Write(buf, binary.LittleEndian, uint32(len("model-a")))
	buf.WriteString("model-a")
	buf.Write(rootHash)
	binary.Write(buf, binary.LittleEndian, uint32(100))
	binary.Write(buf, binary.LittleEndian, uint32(3)) // num_leaf_indices
	binary.Write(buf, binary.LittleEndian, uint32(0))
	binary.Write(buf, binary.LittleEndian, uint32(1))
	binary.Write(buf, binary.LittleEndian, uint32(2))
	binary.Write(buf, binary.LittleEndian, int64(1768556941222626000))
	binary.Write(buf, binary.LittleEndian, uint32(len("gonka1test")))
	buf.WriteString("gonka1test")
	binary.Write(buf, binary.LittleEndian, uint32(len("gonka1test")))
	buf.WriteString("gonka1test")

	expectedHash := sha256.Sum256(buf.Bytes())
	expectedHex := fmt.Sprintf("%x", expectedHash)
	assert.Equal(t, expectedHex, string(payload), "Hex payload mismatch")
}

func TestGetPocArtifactsState_RequiresModelID(t *testing.T) {
	server := &Server{artifactStore: artifacts.NewManagedArtifactStore(t.TempDir(), 3)}
	defer server.artifactStore.Close()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/poc/artifacts/state?height=100", nil)
	rec := httptest.NewRecorder()

	err := server.getPocArtifactsState(e.NewContext(req, rec))
	assert.Error(t, err)
}

func TestGetPocArtifactsState_UsesModelScopedStore(t *testing.T) {
	store := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer store.Close()

	modelStore, err := store.GetOrCreateStore(100, "org/model-a")
	assert.NoError(t, err)
	assert.NoError(t, modelStore.Add(1, []byte("artifact-a")))
	assert.NoError(t, modelStore.Flush())

	otherStore, err := store.GetOrCreateStore(100, "model-b")
	assert.NoError(t, err)
	assert.NoError(t, otherStore.Add(2, []byte("artifact-b")))
	assert.NoError(t, otherStore.Flush())

	server := &Server{artifactStore: store}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/poc/artifacts/state?height=100&model_id=org%2Fmodel-a", nil)
	rec := httptest.NewRecorder()

	err = server.getPocArtifactsState(e.NewContext(req, rec))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp PocArtifactsStateResponse
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, int64(100), resp.PocStageStartBlockHeight)
	assert.Equal(t, "org/model-a", resp.ModelId)
	assert.Equal(t, uint32(1), resp.Count)
	assert.NotEmpty(t, resp.RootHash)
}

func TestPostPocProofs_UsesModelScopedStore(t *testing.T) {
	store := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer store.Close()

	modelAStore, err := store.GetOrCreateStore(100, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, modelAStore.Add(1, []byte{1, 2, 3, 4}))
	assert.NoError(t, modelAStore.Flush())
	modelACount, modelARoot := modelAStore.GetFlushedRoot()

	modelBStore, err := store.GetOrCreateStore(100, "model-b")
	assert.NoError(t, err)
	assert.NoError(t, modelBStore.Add(2, []byte{5, 6, 7, 8}))
	assert.NoError(t, modelBStore.Flush())
	_, modelBRoot := modelBStore.GetFlushedRoot()
	assert.False(t, bytes.Equal(modelARoot, modelBRoot))

	privKey := secp256k1.GenPrivKey()
	pubKeyB64 := base64.StdEncoding.EncodeToString(privKey.PubKey().Bytes())
	queryClient, cleanup := newProofQueryClient(t, &proofQueryServer{pubkeyB64: pubKeyB64})
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)
	mockRecorder.On("GetSignerAddress").Return("participant-signer")
	mockRecorder.On("SignBytes", mock.Anything).Return([]byte("response-signature"), nil)

	server := &Server{
		artifactStore: store,
		recorder:      mockRecorder,
		authzCache:    authzcache.NewAuthzCache(mockRecorder),
	}

	reqBody := &PocProofsRequest{
		PocStageStartBlockHeight: 100,
		ModelId:                  "model-a",
		RootHash:                 base64.StdEncoding.EncodeToString(modelARoot),
		Count:                    StringUint32(modelACount),
		LeafIndices:              []StringUint32{0},
		ValidatorAddress:         "validator-address",
		ValidatorSignerAddress:   "validator-address",
		Timestamp:                StringInt64(time.Now().UnixNano()),
	}
	signature, err := privKey.Sign(buildPocProofsSignPayload(reqBody, modelARoot))
	assert.NoError(t, err)
	reqBody.Signature = base64.StdEncoding.EncodeToString(signature)

	body, err := json.Marshal(reqBody)
	assert.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/poc/proofs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e := echo.New()

	err = server.postPocProofs(e.NewContext(req, rec))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp PocProofsResponse
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp.Proofs, 1)
	assert.Equal(t, uint32(0), resp.Proofs[0].LeafIndex)
	mockRecorder.AssertExpectations(t)
}
