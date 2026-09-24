package transport

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

const (
	gzipEncoding           = "gzip"
	minCompressedBodyBytes = 1 << 10
	// MinGzipBodyBytes is the 1 KiB floor for optional gzip (plan §8.1 /
	// Phase 4 /stats/rpc). Smaller bodies stay identity.
	MinGzipBodyBytes = minCompressedBodyBytes
)

// Pooled here because echo would build a new pool on every request.
var (
	// ResponseCompressionMiddleware compresses a response when the caller asks.
	ResponseCompressionMiddleware = middleware.GzipWithConfig(middleware.GzipConfig{Level: gzip.BestSpeed})
	gzipRequestReaders            = sync.Pool{New: func() any { return new(gzip.Reader) }}
)

// RequestDecompressionMiddleware unwraps a request body; mount it before auth.
func RequestDecompressionMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if !strings.EqualFold(c.Request().Header.Get(echo.HeaderContentEncoding), gzipEncoding) {
			return next(c)
		}
		reader := gzipRequestReaders.Get().(*gzip.Reader)
		compressed := c.Request().Body
		defer func() {
			c.Request().Body = compressed
			_ = compressed.Close()
			_ = reader.Reset(bytes.NewReader(nil))
			gzipRequestReaders.Put(reader)
		}()

		if err := reader.Reset(compressed); err != nil {
			if errors.Is(err, io.EOF) {
				return next(c)
			}
			return echo.NewHTTPError(http.StatusBadRequest, "malformed gzip request body")
		}
		defer reader.Close()

		// Neither header describes the body once the encoding is consumed.
		c.Request().Header.Del(echo.HeaderContentEncoding)
		c.Request().Header.Del(echo.HeaderContentLength)
		c.Request().ContentLength = -1
		c.Request().Body = reader
		return next(c)
	}
}

// AcceptsGzip reports whether the caller asked for gzip (Accept-Encoding).
func AcceptsGzip(h http.Header) bool {
	if h == nil {
		return false
	}
	for _, part := range strings.Split(h.Get("Accept-Encoding"), ",") {
		encoding := strings.TrimSpace(strings.Split(part, ";")[0])
		if strings.EqualFold(encoding, gzipEncoding) {
			return true
		}
	}
	return false
}

// GzipBestSpeed compresses src at BestSpeed. Caller sets Content-Encoding.
func GzipBestSpeed(src []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(src); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}
