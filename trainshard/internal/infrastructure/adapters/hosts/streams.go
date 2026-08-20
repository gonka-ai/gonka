package hosts

import (
	"context"
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
	address, err := hostAddress(base)
	if err != nil {
		return err
	}

	path := toPath(contract.PathShell, req.Shard, req.Node.NodeID)
	request, err := c.request(ctx, http.MethodPost, base, path, vo.NewRequestID(), nil)
	if err != nil {
		return err
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return shared.New("HOST_UNREACHABLE", shared.ErrUnavailable, err.Error())
	}
	defer conn.Close()

	if err := request.Write(conn); err != nil {
		return err
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(conn, session)
		done <- err
	}()

	if _, err := io.Copy(session, conn); err != nil {
		return err
	}
	return <-done
}

func hostAddress(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", shared.New("HOST_ADDRESS", shared.ErrValidation, fmt.Sprintf("host address %q cannot be parsed", base))
	}
	if parsed.Port() != "" {
		return parsed.Host, nil
	}
	if parsed.Scheme == "https" {
		return parsed.Hostname() + ":443", nil
	}
	return parsed.Hostname() + ":80", nil
}
