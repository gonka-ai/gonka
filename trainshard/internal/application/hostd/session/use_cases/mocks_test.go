package usecases_test

import (
	"context"
	"io"
	"strings"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

const shardID = vo.ShardID(7)

var (
	creator = shard.Actor{Address: "gonka1creator"}
	nodeA   = vo.NodeRef{Participant: "gonka1host", NodeID: "node-a"}
	nodeB   = vo.NodeRef{Participant: "gonka1host", NodeID: "node-b"}
	since   = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
)

func activeShard() shard.Shard {
	return shard.Shard{
		ID:              shardID,
		Creator:         "gonka1creator",
		Status:          shard.StatusActive,
		ExpiresAtHeight: 1000,
		Nodes:           []shard.ReservedNode{{Ref: nodeA}},
	}
}

type chainStub struct {
	height vo.Height
	record shard.Shard
	found  bool
	err    error
}

func newChainStub() *chainStub {
	return &chainStub{height: 500, record: activeShard(), found: true}
}

func (c *chainStub) Height(context.Context) (vo.Height, error) { return c.height, c.err }

func (c *chainStub) Shard(context.Context, vo.ShardID) (shard.Shard, bool, error) {
	return c.record, c.found, c.err
}

func (c *chainStub) Reservation(context.Context, vo.NodeRef) (vo.ShardID, bool, error) {
	return shardID, true, nil
}

func (c *chainStub) ActiveShards(context.Context) ([]shard.Shard, error) { return nil, nil }

func (c *chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{}, nil
}

type pipe struct {
	io.Reader
	io.Writer
}

type sessionLogStub struct {
	kept   *strings.Builder
	closed bool
	err    error
}

func newSessionLogStub() *sessionLogStub {
	return &sessionLogStub{kept: &strings.Builder{}}
}

func (s *sessionLogStub) Record(context.Context, vo.ShardID, vo.NodeRef, time.Time) (io.WriteCloser, error) {
	if s.err != nil {
		return nil, s.err
	}
	return sink{log: s}, nil
}

type sink struct {
	log *sessionLogStub
}

func (s sink) Write(p []byte) (int, error) { return s.log.kept.Write(p) }

func (s sink) Close() error {
	s.log.closed = true
	return nil
}

type streamsStub struct {
	logs   *run.LogRequest
	exec   *run.ExecRequest
	output string
}

func (s *streamsStub) Logs(_ context.Context, req run.LogRequest, out io.Writer) error {
	s.logs = &req
	_, err := io.WriteString(out, s.output)
	return err
}

func (s *streamsStub) Shell(_ context.Context, req run.ExecRequest, session io.ReadWriter) error {
	s.exec = &req
	_, err := io.Copy(session, session)
	return err
}
