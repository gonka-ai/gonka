package teeverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	pb "github.com/google/go-tdx-guest/proto/tdx"
	"github.com/google/go-tdx-guest/testing/testdata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var tdxFixtureTime = time.Date(2023, time.July, 1, 0, 0, 0, 0, time.UTC)

func tdxFixtureClock() time.Time { return tdxFixtureTime }

func TestIntelTDXLite_ProviderName(t *testing.T) {
	assert.Equal(t, "intel-tdx-lite", IntelTDXLiteVerifier{}.Provider())
}

func TestIntelTDXLite_VerifyCrypto_AcceptsRealQuote(t *testing.T) {
	q, err := verifyTDXQuoteCrypto(testdata.RawQuote, tdxFixtureTime)
	require.NoError(t, err)
	require.NotNil(t, q)
	require.NotNil(t, q.TdQuoteBody)
	assert.Len(t, q.TdQuoteBody.MrTd, tdxMrTdLen)
	assert.Len(t, q.TdQuoteBody.ReportData, tdxReportDataLen)
}

func TestIntelTDXLite_VerifyCrypto_RejectsTamperedQuote(t *testing.T) {
	tampered := bytes.Clone(testdata.RawQuote)
	tampered[600] ^= 0xFF

	_, err := verifyTDXQuoteCrypto(tampered, tdxFixtureTime)
	require.Error(t, err)
}

func TestIntelTDXLite_VerifyCrypto_RejectsGarbage(t *testing.T) {
	_, err := verifyTDXQuoteCrypto([]byte("not-a-tdx-quote-at-all"), tdxFixtureTime)
	require.Error(t, err)
}

func TestIntelTDXLite_VerifyCrypto_RejectsFutureValidationTime(t *testing.T) {
	farFuture := time.Date(2053, time.July, 1, 0, 0, 0, 0, time.UTC)
	_, err := verifyTDXQuoteCrypto(testdata.RawQuote, farFuture)
	require.Error(t, err)
}

func TestIntelTDXLite_ReportDataBinding_Accepts(t *testing.T) {
	pk := bytes.Repeat([]byte{0xAB}, tdxPublicKeyLen)
	expected := BindReportData(pk)

	q := &pb.QuoteV4{TdQuoteBody: &pb.TDQuoteBody{ReportData: expected[:]}}
	require.NoError(t, verifyTDXReportDataBinding(q, pk))
}

func TestIntelTDXLite_ReportDataBinding_RejectsMismatch(t *testing.T) {
	pk := bytes.Repeat([]byte{0xAB}, tdxPublicKeyLen)
	other := bytes.Repeat([]byte{0xCD}, tdxPublicKeyLen)
	expectedForOther := BindReportData(other)

	q := &pb.QuoteV4{TdQuoteBody: &pb.TDQuoteBody{ReportData: expectedForOther[:]}}
	err := verifyTDXReportDataBinding(q, pk)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "report_data does not bind")
}

func TestIntelTDXLite_ReportDataBinding_RejectsTrailingNoise(t *testing.T) {
	pk := bytes.Repeat([]byte{0xAB}, tdxPublicKeyLen)
	noisy := BindReportData(pk)
	noisy[40] = 0x01

	q := &pb.QuoteV4{TdQuoteBody: &pb.TDQuoteBody{ReportData: noisy[:]}}
	err := verifyTDXReportDataBinding(q, pk)
	require.Error(t, err)
}

func TestIntelTDXLite_ReportDataBinding_RejectsWrongLength(t *testing.T) {
	pk := bytes.Repeat([]byte{0xAB}, tdxPublicKeyLen)
	q := &pb.QuoteV4{TdQuoteBody: &pb.TDQuoteBody{ReportData: make([]byte, 32)}}
	err := verifyTDXReportDataBinding(q, pk)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "report_data must be 64 bytes")
}

func TestIntelTDXLite_Verify_RejectsWrongPublicKeyLength(t *testing.T) {
	v := IntelTDXLiteVerifier{}
	_, err := v.Verify(context.Background(), VerifyRequest{
		PublicKey: make([]byte, 16),
		Quote:     testdata.RawQuote,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "X25519")
}

func TestIntelTDXLite_Verify_RejectsEmptyQuote(t *testing.T) {
	v := IntelTDXLiteVerifier{}
	_, err := v.Verify(context.Background(), VerifyRequest{
		PublicKey: make([]byte, tdxPublicKeyLen),
		Quote:     nil,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quote is required")
}

func TestIntelTDXLite_Verify_FixtureFailsBindingForRandomKey(t *testing.T) {
	v := IntelTDXLiteVerifier{Now: tdxFixtureClock}
	_, err := v.Verify(context.Background(), VerifyRequest{
		PublicKey: bytes.Repeat([]byte{0x01}, tdxPublicKeyLen),
		Quote:     testdata.RawQuote,
	})
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "report_data does not bind") ||
			strings.Contains(err.Error(), "report_data must be"),
		"expected binding failure, got: %v", err)
}

func TestBindReportData_Layout(t *testing.T) {
	pk := bytes.Repeat([]byte{0xAB}, tdxPublicKeyLen)
	rd := BindReportData(pk)
	require.Len(t, rd, tdxReportDataLen)

	expected := sha256.Sum256(pk)
	assert.Equal(t, expected[:], rd[:tdxReportDataPrefix])
	assert.Equal(t, make([]byte, tdxReportDataLen-tdxReportDataPrefix), rd[tdxReportDataPrefix:])
}

func TestIntelTDXVerifier_StrictNotImplemented(t *testing.T) {
	v := IntelTDXVerifier{}
	assert.Equal(t, "intel-tdx", v.Provider())
	_, err := v.Verify(context.Background(), VerifyRequest{
		PublicKey: make([]byte, tdxPublicKeyLen),
		Quote:     testdata.RawQuote,
	})
	require.ErrorIs(t, err, ErrIntelTDXNotImplemented)
}
