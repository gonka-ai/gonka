package hosts

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

// ctrl-d three times on the pty: the first only flushes a line left without its newline, the shell
// may take the second as the end of that line, and a program it runs may take one for itself
var endOfInput = []byte{0x04, 0x04, 0x04}

func (c *Client) Logs(ctx context.Context, host vo.Host, req run.LogRequest, out io.Writer) error {
	body := contract.LogsRequest{Tail: req.Tail}
	if !req.Since.IsZero() {
		body.Since = req.Since.UTC().Format(time.RFC3339)
	}

	path := toPath(contract.PathLogs, req.Shard, req.Node.NodeID)
	return c.stream(ctx, host, http.MethodPost, path, vo.NewRequestID(), body, out)
}

func (c *Client) Shell(ctx context.Context, host vo.Host, req run.ExecRequest, session io.ReadWriter) (err error) {
	base, err := baseURL(host)
	if err != nil {
		return err
	}
	address, secure, err := hostAddress(base)
	if err != nil {
		return err
	}

	path := toPath(contract.PathShell, req.Shard, req.Node.NodeID)
	request, err := c.request(ctx, host.Participant, http.MethodPost, base, path, vo.NewRequestID(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", contract.ShellProtocol)

	conn, err := dial(ctx, address, secure)
	if err != nil {
		return shared.New("HOST_UNREACHABLE", shared.ErrUnavailable, err.Error())
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()

	if err := request.Write(conn); err != nil {
		return err
	}

	// output sent right behind the 101 is already buffered here, and a 101 has no body to read it from
	reader := bufio.NewReader(conn)
	answer, err := http.ReadResponse(reader, request)
	if err != nil {
		return shared.New("HOST_ANSWER", shared.ErrUnavailable, err.Error())
	}
	defer answer.Body.Close()
	if answer.StatusCode != http.StatusSwitchingProtocols {
		var envelope contract.Envelope
		if err := json.NewDecoder(io.LimitReader(answer.Body, maxAnswerBytes)).Decode(&envelope); err != nil {
			return toError(answer.StatusCode, nil)
		}
		return toError(answer.StatusCode, envelope.Error)
	}
	if !strings.EqualFold(answer.Header.Get("Upgrade"), contract.ShellProtocol) {
		return shared.New("HOST_ANSWER", shared.ErrUnavailable, fmt.Sprintf("host switched to %q, not a shell", answer.Header.Get("Upgrade")))
	}

	// the end of input goes inside the stream, never as a half close: a proxy drops an upgraded
	// connection at the first half close, output still on its way
	typed := &failureNoted{Reader: session}
	go func() {
		if _, err := io.Copy(conn, typed); err == nil {
			_, _ = conn.Write(endOfInput)
		} else if typed.failure() != nil {
			// a shell with no input left is hung, not finished: end it where the caller can see why
			_ = conn.Close()
		}
	}()

	_, err = io.Copy(session, reader)
	if failure := typed.failure(); failure != nil {
		return fmt.Errorf("reading what was typed: %w", failure)
	}
	return err
}

// failureNoted remembers a read that failed, which a copy cannot tell from a write that did
type failureNoted struct {
	io.Reader
	failed atomic.Pointer[error]
}

func (f *failureNoted) Read(p []byte) (int, error) {
	n, err := f.Reader.Read(p)
	if err != nil && err != io.EOF {
		f.failed.Store(&err)
	}
	return n, err
}

func (f *failureNoted) failure() error {
	if err := f.failed.Load(); err != nil {
		return *err
	}
	return nil
}

func dial(ctx context.Context, address string, secure bool) (net.Conn, error) {
	if secure {
		return (&tls.Dialer{}).DialContext(ctx, "tcp", address)
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", address)
}

func hostAddress(base string) (address string, secure bool, err error) {
	parsed, parseErr := url.Parse(base)
	if parseErr != nil {
		return "", false, shared.New("HOST_ADDRESS", shared.ErrValidation, fmt.Sprintf("host address %q cannot be parsed", base))
	}
	secure = parsed.Scheme == "https"
	if parsed.Port() != "" {
		return parsed.Host, secure, nil
	}
	if secure {
		return net.JoinHostPort(parsed.Hostname(), "443"), true, nil
	}
	return net.JoinHostPort(parsed.Hostname(), "80"), false, nil
}
