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
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type halfCloser interface {
	CloseWrite() error
}

func (c *Client) Logs(ctx context.Context, host vo.Host, req run.LogRequest, out io.Writer) error {
	body := contract.LogsRequest{Tail: req.Tail}
	if !req.Since.IsZero() {
		body.Since = req.Since.UTC().Format(time.RFC3339)
	}

	path := toPath(contract.PathLogs, req.Shard, req.Node.NodeID)
	return c.stream(ctx, host, http.MethodPost, path, vo.NewRequestID(), body, out)
}

func (c *Client) Shell(ctx context.Context, host vo.Host, req run.ExecRequest, session io.ReadWriter) error {
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

	if err := request.Write(conn); err != nil {
		return err
	}

	// the output that came in with the answer is already in this reader, and a 101 has no body
	// to read it from, so the session is read from here rather than from the answer
	reader := bufio.NewReader(conn)
	answer, err := http.ReadResponse(reader, request)
	if err != nil {
		return shared.New("HOST_ANSWER", shared.ErrUnavailable, err.Error())
	}
	defer answer.Body.Close()
	if answer.StatusCode != http.StatusSwitchingProtocols {
		var envelope contract.Envelope
		if err := json.NewDecoder(answer.Body).Decode(&envelope); err != nil {
			return toError(answer.StatusCode, nil)
		}
		return toError(answer.StatusCode, envelope.Error)
	}
	if !strings.EqualFold(answer.Header.Get("Upgrade"), contract.ShellProtocol) {
		return shared.New("HOST_ANSWER", shared.ErrUnavailable, fmt.Sprintf("host switched to %q, not a shell", answer.Header.Get("Upgrade")))
	}

	go func() {
		_, _ = io.Copy(conn, session)
		if half, ok := conn.(halfCloser); ok {
			_ = half.CloseWrite()
		}
	}()

	_, err = io.Copy(session, reader)
	return err
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
