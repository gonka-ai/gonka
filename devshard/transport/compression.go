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
)

// Pooled here because echo would build a new pool on every request.
var (
	// ResponseCompressionMiddleware compresses a response when the caller asks.
	ResponseCompressionMiddleware = middleware.GzipWithConfig(middleware.GzipConfig{Level: gzip.BestSpeed})
	gzipRequestReaders            = sync.Pool{New: func() any { return new(gzip.Reader) }}
	requestCompressors            = sync.Pool{New: func() any {
		writer, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return writer
	}}
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

// encodeRequestBody gzips a body worth compressing and names the encoding.
func (c *HTTPClient) encodeRequestBody(body []byte) ([]byte, string) {
	if !c.config.CompressRequestBodies || len(body) < minCompressedBodyBytes {
		return body, ""
	}
	writer := requestCompressors.Get().(*gzip.Writer)
	var compressed bytes.Buffer
	writer.Reset(&compressed)
	_, _ = writer.Write(body)
	_ = writer.Close()
	writer.Reset(io.Discard)
	requestCompressors.Put(writer)
	return compressed.Bytes(), gzipEncoding
}
