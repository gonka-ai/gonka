package transport

import (
	"compress/gzip"
	"errors"
	"io"
	"math"

	"connectrpc.com/connect"
)

// errGzipInflateCap is returned once maxBytes+1 decoded bytes have already
// been handed to connect. connect then drains the decompressor to measure an
// over-cap message; failing that Read skips the rest of the gzip stream.
var errGzipInflateCap = errors.New("gzip inflate cap")

// cappedGzipReader is a gzip.Reader that stops after allow decoded bytes.
// allow is WithReadMaxBytes+1, which is what connect's LimitReader consumes
// before it decides the message is over the cap.
type cappedGzipReader struct {
	z     gzip.Reader
	allow int64
	n     int64
}

func newCappedGzipReader(maxBytes int) *cappedGzipReader {
	allow := int64(maxBytes) + 1
	if maxBytes <= 0 {
		// WithReadMaxBytes(0) means unlimited. Do not cap at 1 byte.
		allow = math.MaxInt64
	}
	return &cappedGzipReader{allow: allow}
}

func (r *cappedGzipReader) Read(p []byte) (int, error) {
	if r.n >= r.allow {
		return 0, errGzipInflateCap
	}
	if remain := r.allow - r.n; int64(len(p)) > remain {
		p = p[:remain]
	}
	n, err := r.z.Read(p)
	r.n += int64(n)
	return n, err
}

func (r *cappedGzipReader) Close() error { return r.z.Close() }

func (r *cappedGzipReader) Reset(src io.Reader) error {
	r.n = 0
	return r.z.Reset(src)
}

func gzipReadCapConstructors(maxBytes int) (func() connect.Decompressor, func() connect.Compressor) {
	return func() connect.Decompressor { return newCappedGzipReader(maxBytes) },
		func() connect.Compressor { return gzip.NewWriter(io.Discard) }
}

// WithGzipReadCap replaces connect-go's default gzip decompressor. The stock
// reader is drained to EOF when a message exceeds WithReadMaxBytes, so a
// compressed body under the cap can still expand without a decode limit.
// maxBytes is the same value passed to WithReadMaxBytes. The compressor stays
// the standard library writer so WithSendGzip still resolves.
func WithGzipReadCap(maxBytes int) connect.HandlerOption {
	dec, comp := gzipReadCapConstructors(maxBytes)
	return connect.WithCompression(gzipEncoding, dec, comp)
}

// withGzipReadCap is WithGzipReadCap for clients. WithCompression is a
// HandlerOption; clients register the same pool with WithAcceptCompression.
func withGzipReadCap(maxBytes int) connect.ClientOption {
	dec, comp := gzipReadCapConstructors(maxBytes)
	return connect.WithAcceptCompression(gzipEncoding, dec, comp)
}
