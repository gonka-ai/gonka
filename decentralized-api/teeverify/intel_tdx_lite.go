package teeverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-tdx-guest/abi"
	pb "github.com/google/go-tdx-guest/proto/tdx"
	"github.com/google/go-tdx-guest/verify"
)

const (
	IntelTDXLiteProvider  = "intel-tdx-lite"
	IntelTDXLiteTcbStatus = "lite"
)

const (
	tdxPublicKeyLen     = 32
	tdxReportDataLen    = 64
	tdxReportDataPrefix = sha256.Size
	tdxMrTdLen          = 48
)

type IntelTDXLiteVerifier struct {
	Now func() time.Time
}

func (IntelTDXLiteVerifier) Provider() string { return IntelTDXLiteProvider }

func (v IntelTDXLiteVerifier) Verify(_ context.Context, req VerifyRequest) (*Report, error) {
	if len(req.PublicKey) != tdxPublicKeyLen {
		return nil, fmt.Errorf("teeverify(intel-tdx-lite): public key must be %d bytes (X25519), got %d", tdxPublicKeyLen, len(req.PublicKey))
	}
	if len(req.Quote) == 0 {
		return nil, errors.New("teeverify(intel-tdx-lite): quote is required")
	}

	q, err := verifyTDXQuoteCrypto(req.Quote, v.now())
	if err != nil {
		return nil, err
	}

	if err := verifyTDXReportDataBinding(q, req.PublicKey); err != nil {
		return nil, err
	}

	mrtd := q.TdQuoteBody.MrTd
	if len(mrtd) != tdxMrTdLen {
		return nil, fmt.Errorf("teeverify(intel-tdx-lite): mr_td must be %d bytes, got %d", tdxMrTdLen, len(mrtd))
	}

	qh := sha256.Sum256(req.Quote)
	return &Report{
		Measurement: hex.EncodeToString(mrtd),
		TcbStatus:   IntelTDXLiteTcbStatus,
		QuoteHash:   qh[:],
	}, nil
}

func (v IntelTDXLiteVerifier) now() time.Time {
	if v.Now == nil {
		return time.Now()
	}
	return v.Now()
}

func verifyTDXQuoteCrypto(raw []byte, now time.Time) (*pb.QuoteV4, error) {
	parsed, err := abi.QuoteToProto(raw)
	if err != nil {
		return nil, fmt.Errorf("teeverify(intel-tdx-lite): parse quote: %w", err)
	}
	q, ok := parsed.(*pb.QuoteV4)
	if !ok {
		return nil, fmt.Errorf("teeverify(intel-tdx-lite): unsupported quote type %T (only TDX V4 supported)", parsed)
	}

	opts := verify.DefaultOptions()
	opts.GetCollateral = false
	opts.CheckRevocations = false
	opts.Now = now

	if err := verify.TdxQuote(q, opts); err != nil {
		return nil, fmt.Errorf("teeverify(intel-tdx-lite): quote verify: %w", err)
	}
	if q.TdQuoteBody == nil {
		return nil, errors.New("teeverify(intel-tdx-lite): missing td_quote_body")
	}
	return q, nil
}

func verifyTDXReportDataBinding(q *pb.QuoteV4, publicKey []byte) error {
	rd := q.TdQuoteBody.ReportData
	if len(rd) != tdxReportDataLen {
		return fmt.Errorf("teeverify(intel-tdx-lite): report_data must be %d bytes, got %d", tdxReportDataLen, len(rd))
	}
	expected := BindReportData(publicKey)
	if !bytes.Equal(rd, expected[:]) {
		return errors.New("teeverify(intel-tdx-lite): report_data does not bind to sha256(public_key)")
	}
	return nil
}

func BindReportData(publicKey []byte) [tdxReportDataLen]byte {
	var rd [tdxReportDataLen]byte
	h := sha256.Sum256(publicKey)
	copy(rd[:tdxReportDataPrefix], h[:])
	return rd
}
