package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/calculations"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"devshard/observability"
)

type payloadAuthQueryClient struct {
	inferenceTypes.QueryClient
	grantees []*inferenceTypes.Grantee
	queries  int
}

func (c *payloadAuthQueryClient) AccountByAddress(context.Context, *inferenceTypes.QueryAccountByAddressRequest, ...grpc.CallOption) (*inferenceTypes.QueryAccountByAddressResponse, error) {
	c.queries++
	return &inferenceTypes.QueryAccountByAddressResponse{}, nil
}

func (c *payloadAuthQueryClient) GranteesByMessageType(context.Context, *inferenceTypes.QueryGranteesByMessageTypeRequest, ...grpc.CallOption) (*inferenceTypes.QueryGranteesByMessageTypeResponse, error) {
	c.queries++
	return &inferenceTypes.QueryGranteesByMessageTypeResponse{Grantees: c.grantees}, nil
}

type payloadAuthRecorder struct {
	qc *payloadAuthQueryClient
}

func (r *payloadAuthRecorder) NewInferenceQueryClient() inferenceTypes.QueryClient { return r.qc }
func (r *payloadAuthRecorder) GetAccountAddress() string                           { return "" }
func (r *payloadAuthRecorder) GetSignerAddress() string                            { return "" }
func (r *payloadAuthRecorder) SignBytes([]byte) (string, error)                    { return "", nil }

type payloadKeySigner struct {
	key *secp256k1.PrivKey
}

func (s payloadKeySigner) SignBytes(data []byte) (string, error) {
	sig, err := s.key.Sign(data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

type signedPayloadFixture struct {
	mgr  *HostManager
	br   *countingWarmBridge
	qc   *payloadAuthQueryClient
	warm string
	key  *secp256k1.PrivKey
}

func newSignedPayloadFixture(t *testing.T) signedPayloadFixture {
	t.Helper()
	key := secp256k1.GenPrivKey()
	warm, err := sdk.Bech32ifyAddressBytes("gonka", key.PubKey().Address())
	require.NoError(t, err)
	qc := &payloadAuthQueryClient{grantees: []*inferenceTypes.Grantee{{
		Address: warm,
		PubKey:  base64.StdEncoding.EncodeToString(key.PubKey().Bytes()),
	}}}
	br := &countingWarmBridge{warm: warm}
	mgr := newRosterManager(t, 7, br)
	mgr.recorder = &payloadAuthRecorder{qc: qc}
	return signedPayloadFixture{mgr: mgr, br: br, qc: qc, warm: warm, key: key}
}

func (f signedPayloadFixture) input(t *testing.T, addr string) PayloadAuthInput {
	t.Helper()
	in := PayloadAuthInput{InferenceID: "1", ValidatorAddress: addr, Timestamp: time.Now().UnixNano(), EpochID: 7}
	sig, err := calculations.Sign(payloadKeySigner{key: f.key}, calculations.SignatureComponents{
		Payload:         in.InferenceID,
		EpochId:         in.EpochID,
		Timestamp:       in.Timestamp,
		TransferAddress: in.ValidatorAddress,
	}, calculations.Developer)
	require.NoError(t, err)
	in.Signature = sig
	return in
}

func TestAuthenticatePayloadAuth_SignedWarmKeyIsAccepted(t *testing.T) {
	f := newSignedPayloadFixture(t)
	group, _ := payloadAuthGroup(t)

	in := f.input(t, f.warm)
	require.LessOrEqual(t, len(in.Signature), maxPayloadSignatureLen)
	epoch, reason, err := f.mgr.authenticatePayloadAuth(context.Background(), group, in)
	require.NoError(t, err)
	require.Equal(t, observability.ReasonOK, reason)
	require.Equal(t, uint64(7), epoch)
}

func TestAuthenticatePayloadAuth_OversizedSignatureIsRejectedBeforeLookup(t *testing.T) {
	f := newSignedPayloadFixture(t)
	group, _ := payloadAuthGroup(t)

	in := f.input(t, f.warm)
	in.Signature += strings.Repeat("A", 10<<20)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, reason, err := f.mgr.authenticatePayloadAuth(context.Background(), group, in)
	require.Error(t, err)
	require.Equal(t, observability.ReasonInvalidSignature, reason)
	require.Less(t, buf.Len(), 4096)
	require.Zero(t, f.qc.queries)
	require.Zero(t, f.br.warmCalls.Load())
}

func TestAuthenticatePayloadAuth_SignedUppercaseWarmKeyIsRejected(t *testing.T) {
	f := newSignedPayloadFixture(t)
	group, _ := payloadAuthGroup(t)

	_, reason, err := f.mgr.authenticatePayloadAuth(context.Background(), group, f.input(t, strings.ToUpper(f.warm)))
	require.Error(t, err)
	require.Equal(t, observability.ReasonNotGroupMember, reason)
	require.Zero(t, f.qc.queries)
}

func TestServeGetPayload_InvalidInferenceIDIsRejectedBeforeAuth(t *testing.T) {
	group, member := payloadAuthGroup(t)
	for name, id := range map[string]string{
		"not a number":  "abc",
		"negative":      "-1",
		"overflow":      "18446744073709551616",
		"oversized":     strings.Repeat("9", 10<<20),
		"spaces":        " 1",
		"leading zeros": strings.Repeat("0", 10<<20) + "1",
		"21 digits":     "000000000000000000001",
	} {
		t.Run(name, func(t *testing.T) {
			br := &countingWarmBridge{}
			mgr := newRosterManager(t, 7, br)
			in := payloadAuthInput(mustGenerateKey(t).Address())
			in.InferenceID = id

			_, err := mgr.serveGetPayload(context.Background(), "9100", group, in)
			var he *echo.HTTPError
			require.ErrorAs(t, err, &he)
			require.Equal(t, http.StatusBadRequest, he.Code)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(connectFromEcho(err)))
			require.Zero(t, br.warmCalls.Load())
		})
	}

	t.Run("valid id reaches auth", func(t *testing.T) {
		br := &countingWarmBridge{}
		mgr := newRosterManager(t, 7, br)
		_, err := mgr.serveGetPayload(context.Background(), "9100", group, payloadAuthInput(strings.ToUpper(member.Address())))
		var he *echo.HTTPError
		require.ErrorAs(t, err, &he)
		require.Equal(t, http.StatusUnauthorized, he.Code)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(connectFromEcho(err)))
		require.Equal(t, int32(len(group)), br.warmCalls.Load())
	})
}
