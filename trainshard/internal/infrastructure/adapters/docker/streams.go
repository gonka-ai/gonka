package docker

import (
	"context"
	"errors"
	"io"
	"strconv"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"trainshard/internal/domain/run"
	"trainshard/internal/utils/streamx"
)

func (c *Client) Logs(ctx context.Context, req run.LogRequest, out io.Writer) error {
	options := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true}
	if req.Tail > 0 {
		options.Tail = strconv.Itoa(req.Tail)
	}
	if !req.Since.IsZero() {
		options.Since = strconv.FormatInt(req.Since.Unix(), 10)
	}

	logs, err := c.engine.ContainerLogs(ctx, containerName(req.Shard, req.Node), options)
	if err != nil {
		return err
	}
	defer logs.Close()

	bounded := streamx.NewBounded(ctx, out, c.cfg.LogBufferBytes)
	_, copied := stdcopy.StdCopy(bounded, bounded, logs)
	return errors.Join(copied, bounded.Close())
}

func (c *Client) Shell(ctx context.Context, req run.ExecRequest, session io.ReadWriter) error {
	created, err := c.engine.ExecCreate(ctx, containerName(req.Shard, req.Node), client.ExecCreateOptions{
		User:         c.cfg.User,
		WorkingDir:   workdir,
		Cmd:          []string{"/bin/sh"},
		TTY:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	attached, err := c.engine.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return err
	}
	defer attached.Close()

	// the end of the input is the end of the typing, not of the session: a terminal stream carries
	// no half close, so the output is read until the shell itself exits. A caller that hung up or
	// half closed will type nothing more, so the shell is given the end of its input the way ctrl-d
	// gives it, or it would wait on the connection for good
	go func() {
		io.Copy(attached.Conn, session)
		attached.Conn.Write([]byte{0x04, 0x04, 0x04})
	}()

	_, err = io.Copy(session, attached.Reader)
	return err
}
