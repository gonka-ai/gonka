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
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type halfCloser interface {
	CloseWrite() error
}

func (c *Client) Logs(ctx context.Context, participant vo.Participant, req run.LogRequest, out io.Writer) error {
	body := contract.LogsRequest{Tail: req.Tail}
	if !req.Since.IsZero() {
		body.Since = req.Since.UTC().Format(time.RFC3339)
	}

	path := toPath(contract.PathLogs, req.Shard, req.Node.NodeID)
	return c.stream(ctx, participant, http.MethodPost, path, vo.NewRequestID(), body, out)
}

func (c *Client) Shell(ctx context.Context, participant vo.Participant, req run.ExecRequest, session io.ReadWriter) error {
	base, err := c.directory.baseURL(participant)
	if err != nil {
		return err
	}
	address, secure, err := hostAddress(base)
	if err != nil {
		return err
	}

	path := toPath(contract.PathShell, req.Shard, req.Node.NodeID)
	request, err := c.request(ctx, http.MethodPost, base, path, vo.NewRequestID(), nil)
	if err != nil {
		return err
	}

	conn, err := dial(ctx, address, secure)
	if err != nil {
		return shared.New("HOST_UNREACHABLE", shared.ErrUnavailable, err.Error())
	}
	defer conn.Close()

	if err := request.Write(conn); err != nil {
		return err
	}

	go func() {
		_, _ = io.Copy(conn, session)
		if half, ok := conn.(halfCloser); ok {
			_ = half.CloseWrite()
		}
	}()

	answer, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return shared.New("HOST_ANSWER", shared.ErrUnavailable, err.Error())
	}
	defer answer.Body.Close()
	if answer.StatusCode != http.StatusOK {
		var envelope contract.Envelope
		if err := json.NewDecoder(answer.Body).Decode(&envelope); err != nil {
			return toError(answer.StatusCode, nil)
		}
		return toError(answer.StatusCode, envelope.Error)
	}

	_, err = io.Copy(session, answer.Body)
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
		return parsed.Hostname() + ":443", true, nil
	}
	return parsed.Hostname() + ":80", false, nil
}
