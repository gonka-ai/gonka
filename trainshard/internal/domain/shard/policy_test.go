package shard_test

import (
	"errors"
	"testing"
	"time"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

var (
	now      = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	creator  = vo.Address("gonka1creator")
	runKey   = vo.Address("gonka1runkey")
	stranger = vo.Address("gonka1stranger")
	nodeA    = vo.NodeRef{Participant: "gonka1host", NodeID: "node-a"}
	nodeB    = vo.NodeRef{Participant: "gonka1host", NodeID: "node-b"}
)

const height = vo.Height(500)

func activeShard() shard.Shard {
	return shard.Shard{
		ID:              7,
		Creator:         creator,
		RunKey:          runKey,
		Status:          shard.StatusActive,
		ExpiresAtHeight: 1000,
		Nodes:           []shard.ReservedNode{{Ref: nodeA, ModelID: "model-1"}},
	}
}

func command() shard.Command {
	return shard.Command{
		Shard:     7,
		Node:      nodeA,
		Actor:     shard.Actor{Address: creator},
		RequestID: "req-1",
		Deadline:  now.Add(time.Minute),
	}
}

func TestCanApply(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*shard.Command, *shard.Shard)
		wantErr error
	}{
		{
			name:   "authorized command on an active shard",
			mutate: func(*shard.Command, *shard.Shard) {},
		},
		{
			name:    "command for another shard",
			mutate:  func(c *shard.Command, _ *shard.Shard) { c.Shard = 8 },
			wantErr: shard.ErrShardMismatch,
		},
		{
			name:    "settled shard",
			mutate:  func(_ *shard.Command, s *shard.Shard) { s.Status = shard.StatusSettled },
			wantErr: shard.ErrShardClosed,
		},
		{
			name:    "shard past its expiry height",
			mutate:  func(_ *shard.Command, s *shard.Shard) { s.ExpiresAtHeight = height },
			wantErr: shard.ErrShardClosed,
		},
		{
			name:    "node not reserved in this shard",
			mutate:  func(c *shard.Command, _ *shard.Shard) { c.Node = nodeB },
			wantErr: shard.ErrNodeNotReserved,
		},
		{
			name:   "run key named by the creator",
			mutate: func(c *shard.Command, _ *shard.Shard) { c.Actor = shard.Actor{Address: runKey} },
		},
		{
			name:    "actor that is neither creator nor run key",
			mutate:  func(c *shard.Command, _ *shard.Shard) { c.Actor = shard.Actor{Address: stranger} },
			wantErr: shard.ErrNotAuthorized,
		},
		{
			name:    "deadline already passed",
			mutate:  func(c *shard.Command, _ *shard.Shard) { c.Deadline = now.Add(-time.Second) },
			wantErr: shard.ErrDeadlinePassed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			cmd, sh := command(), activeShard()
			tc.mutate(&cmd, &sh)

			err := shard.CanApply(cmd, sh, now, height)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestCanObserveIgnoresTheDeadline(t *testing.T) {

	cmd, sh := command(), activeShard()
	cmd.Deadline = now.Add(-time.Hour)

	err := shard.CanObserve(cmd, sh, height)

	if err != nil {
		t.Fatalf("a read must not expire, got %v", err)
	}
}

func TestCanApplyMeshRequiresADrainedNode(t *testing.T) {

	cmd, sh := command(), activeShard()

	notDrained := shard.CanApplyMesh(cmd, sh, false, now, height)
	drained := shard.CanApplyMesh(cmd, sh, true, now, height)

	if !errors.Is(notDrained, shard.ErrNodeNotPrepared) {
		t.Fatalf("got %v, want %v", notDrained, shard.ErrNodeNotPrepared)
	}
	if drained != nil {
		t.Fatalf("drained node must be allowed, got %v", drained)
	}
}

func TestRefusalsCarryStableCodes(t *testing.T) {

	cmd, sh := command(), activeShard()
	cmd.Actor = shard.Actor{Address: stranger}

	code := shared.CodeOf(shard.CanApply(cmd, sh, now, height))

	if code != "NOT_AUTHORIZED" {
		t.Fatalf("got code %q, want NOT_AUTHORIZED", code)
	}
}
