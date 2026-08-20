package docker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"trainshard/internal/domain/run"
	"trainshard/internal/utils/streamx"
)

const readBuffer = 32 << 10

func (c *Client) Logs(ctx context.Context, req run.LogRequest, out io.Writer) error {
	query := url.Values{"stdout": {"1"}, "stderr": {"1"}}
	if req.Tail > 0 {
		query.Set("tail", strconv.Itoa(req.Tail))
	}
	if !req.Since.IsZero() {
		query.Set("since", strconv.FormatInt(req.Since.Unix(), 10))
	}

	name := containerName(req.Shard, req.Node)
	body, err := c.stream(ctx, http.MethodGet, "/containers/"+name+"/logs", query, nil)
	if err != nil {
		return err
	}
	defer body.Close()

	bounded := streamx.NewBounded(ctx, out, c.cfg.LogBufferBytes)
	return errors.Join(demultiplex(body, bounded), bounded.Close())
}

func (c *Client) Shell(ctx context.Context, req run.ExecRequest, session io.ReadWriter) error {
	name := containerName(req.Shard, req.Node)

	var created struct {
		ID string `json:"Id"`
	}
	if _, err := c.call(ctx, http.MethodPost, "/containers/"+name+"/exec", nil, execRequest{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		User:         c.cfg.User,
		WorkingDir:   workdir,
		Cmd:          []string{"/bin/sh"},
	}, &created); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, reader, err := c.attach(ctx, "/exec/"+created.ID+"/start", execStart{Tty: true})
	if err != nil {
		return err
	}
	defer conn.Close()

	go func() {
		defer cancel()
		io.Copy(conn, session)
	}()

	_, err = io.Copy(session, reader)
	return err
}

func demultiplex(body io.Reader, out io.Writer) error {
	header := make([]byte, 8)
	buffer := make([]byte, readBuffer)

	for {
		if _, err := io.ReadFull(body, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}

		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, err := io.CopyBuffer(out, io.LimitReader(body, size), buffer); err != nil {
			return fmt.Errorf("copy container output: %w", err)
		}
	}
}
