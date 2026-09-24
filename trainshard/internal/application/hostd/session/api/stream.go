package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
)

var (
	errNoHijack  = shared.New("STREAM_UNSUPPORTED", shared.ErrUnavailable, "this server cannot hand over the connection a shell needs")
	errNoUpgrade = shared.New("SHELL_UPGRADE_REQUIRED", shared.ErrValidation, "a shell is opened with Connection: Upgrade and Upgrade: "+contract.ShellProtocol)
)

func asksForShell(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), contract.ShellProtocol) {
		return false
	}
	for _, token := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

type stream struct {
	writer  http.ResponseWriter
	started bool
}

func (s *stream) Write(p []byte) (int, error) {
	if !s.started {
		s.started = true
		s.writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		s.writer.WriteHeader(http.StatusOK)
	}

	n, err := s.writer.Write(p)
	if flusher, ok := s.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return n, err
}

type duplex struct {
	writer  http.ResponseWriter
	once    sync.Once
	conn    net.Conn
	buffer  *bufio.ReadWriter
	err     error
	started bool
}

func (d *duplex) Read(p []byte) (int, error) {
	if err := d.hijack(); err != nil {
		return 0, err
	}
	return d.buffer.Read(p)
}

func (d *duplex) Write(p []byte) (int, error) {
	if err := d.hijack(); err != nil {
		return 0, err
	}
	n, err := d.buffer.Write(p)
	if err != nil {
		return n, err
	}
	return n, d.buffer.Flush()
}

func (d *duplex) hijack() error {
	d.once.Do(func() {
		hijacker, ok := d.writer.(http.Hijacker)
		if !ok {
			d.err = errNoHijack
			return
		}
		conn, buffer, err := hijacker.Hijack()
		if err != nil {
			d.err = fmt.Errorf("hand over the connection: %w", err)
			return
		}
		d.conn, d.buffer, d.started = conn, buffer, true
		_, d.err = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + contract.ShellProtocol + "\r\n\r\n")
		if d.err == nil {
			d.err = buffer.Flush()
		}
	})
	return d.err
}

func (d *duplex) close() {
	if d.conn != nil {
		d.conn.Close()
	}
}
