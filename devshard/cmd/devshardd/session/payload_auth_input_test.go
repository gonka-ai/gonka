package session

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/btcutil/bech32"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/observability"
	"devshard/signing"
	"devshard/types"
)

func TestValidPayloadAddress(t *testing.T) {
	addr := mustGenerateKey(t).Address()
	require.True(t, strings.HasPrefix(addr, "gonka1"))

	corrupt := []byte(addr)
	if corrupt[len(corrupt)-1] == 'q' {
		corrupt[len(corrupt)-1] = 'p'
	} else {
		corrupt[len(corrupt)-1] = 'q'
	}

	cases := []struct {
		name string
		addr string
		ok   bool
	}{
		{"lowercase", addr, true},
		{"uppercase", strings.ToUpper(addr), true},
		{"mixed case", mixedCase(addr), false},
		{"bad checksum", string(corrupt), false},
		{"empty", "", false},
		{"not bech32", "gonka1slot", false},
		{"oversized", "gonka1" + strings.Repeat("q", 10<<20), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.ok, validPayloadAddress(tc.addr))
		})
	}
}

func payloadAuthGroup(t *testing.T) ([]types.SlotAssignment, *signing.Secp256k1Signer) {
	t.Helper()
	member := mustGenerateKey(t)
	return makeGroup([]*signing.Secp256k1Signer{member, mustGenerateKey(t)}), member
}

func payloadAuthInput(addr string) PayloadAuthInput {
	return PayloadAuthInput{
		InferenceID:      "1",
		ValidatorAddress: addr,
		Timestamp:        time.Now().UnixNano(),
		EpochID:          7,
		Signature:        "sig",
	}
}

func TestAuthenticatePayloadAuth_InvalidAddressSkipsWarmKeyLookup(t *testing.T) {
	group, member := payloadAuthGroup(t)

	for name, addr := range map[string]string{
		"oversized":  "gonka1" + strings.Repeat("q", 10<<20),
		"not bech32": "gonka1slot",
		"mixed case": mixedCase(member.Address()),
		"empty":      "",
	} {
		t.Run(name, func(t *testing.T) {
			br := &countingWarmBridge{}
			mgr := newRosterManager(t, 7, br)

			_, reason, err := mgr.authenticatePayloadAuth(context.Background(), group, payloadAuthInput(addr))
			require.Equal(t, observability.ReasonNotGroupMember, reason)
			var he *echo.HTTPError
			require.ErrorAs(t, err, &he)
			require.Equal(t, http.StatusUnauthorized, he.Code)
			require.Zero(t, br.warmCalls.Load())
		})
	}
}

func TestAuthenticatePayloadAuth_UppercaseMemberIsStillRejected(t *testing.T) {
	group, member := payloadAuthGroup(t)
	br := &countingWarmBridge{}
	mgr := newRosterManager(t, 7, br)

	_, reason, err := mgr.authenticatePayloadAuth(context.Background(), group, payloadAuthInput(strings.ToUpper(member.Address())))
	require.Equal(t, observability.ReasonNotGroupMember, reason)
	require.Error(t, err)
	require.Equal(t, int32(len(group)), br.warmCalls.Load())
}

func TestAuthenticatePayloadAuth_ValidAddressReachesLookup(t *testing.T) {
	group, _ := payloadAuthGroup(t)
	br := &countingWarmBridge{}
	mgr := newRosterManager(t, 7, br)

	_, reason, err := mgr.authenticatePayloadAuth(context.Background(), group, payloadAuthInput(mustGenerateKey(t).Address()))
	require.Equal(t, observability.ReasonNotGroupMember, reason)
	require.Error(t, err)
	require.Equal(t, int32(len(group)), br.warmCalls.Load())
}

func TestFindGranterInGroup_ValidWarmKey(t *testing.T) {
	group, _ := payloadAuthGroup(t)
	warm := mustGenerateKey(t).Address()
	br := &countingWarmBridge{warm: warm}
	mgr := newRosterManager(t, 7, br)

	require.True(t, validPayloadAddress(warm))
	granter, err := mgr.findGranterInGroup(warm, group)
	require.NoError(t, err)
	require.Equal(t, group[0].ValidatorAddress, granter)
}

func mixedCase(addr string) string {
	b := []byte(addr)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
			return string(b)
		}
	}
	return addr
}

func bech32OfLen(t *testing.T, total int) string {
	t.Helper()
	data := make([]byte, total-len("gonka1")-6)
	s, err := bech32.Encode("gonka", data)
	require.NoError(t, err)
	require.Len(t, s, total)
	return s
}

func TestValidPayloadAddress_Bounds(t *testing.T) {
	require.True(t, validPayloadAddress(bech32OfLen(t, maxPayloadAddressLen)))
	require.False(t, validPayloadAddress(bech32OfLen(t, maxPayloadAddressLen+1)))
	require.True(t, validPayloadAddress("a12uel5l"))
	require.False(t, validPayloadAddress("gonka1é"+strings.Repeat("q", 40)))
}

func TestValidPayloadSignature(t *testing.T) {
	require.False(t, validPayloadSignature(""))
	require.True(t, validPayloadSignature(strings.Repeat("A", 88)))
	require.True(t, validPayloadSignature(strings.Repeat("A", maxPayloadSignatureLen)))
	require.False(t, validPayloadSignature(strings.Repeat("A", maxPayloadSignatureLen+1)))
}

func TestValidPayloadInferenceID(t *testing.T) {
	require.True(t, validPayloadInferenceID("1"))
	require.True(t, validPayloadInferenceID("18446744073709551615"))
	require.True(t, validPayloadInferenceID("00000000000000000001"))
	require.False(t, validPayloadInferenceID("000000000000000000001"))
	require.False(t, validPayloadInferenceID("18446744073709551616"))
	require.False(t, validPayloadInferenceID(""))
	require.False(t, validPayloadInferenceID("+1"))
}
