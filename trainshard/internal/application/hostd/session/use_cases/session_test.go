package usecases_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	usecases "trainshard/internal/application/hostd/session/use_cases"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/utils/timex"
)

func logsCommand() usecases.LogsCommand {
	return usecases.LogsCommand{
		SessionCommand: usecases.SessionCommand{Shard: shardID, Node: nodeA, Actor: creator},
		Since:          since,
		Tail:           100,
	}
}

func TestLogsReachTheContainerWithWhatWasAskedFor(t *testing.T) {

	chain, streams := newChainStub(), &streamsStub{output: "step 1\nstep 2\n"}
	var out bytes.Buffer

	err := usecases.NewStreamLogsUseCase(chain, streams).Execute(context.Background(), logsCommand(), &out)

	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if out.String() != streams.output {
		t.Fatalf("got %q, want the container output", out.String())
	}
	if streams.logs.Node != nodeA || !streams.logs.Since.Equal(since) || streams.logs.Tail != 100 {
		t.Fatalf("got %+v, want the window the operator asked for", *streams.logs)
	}
}

func TestAStreamIsRefusedBeforeAnyOutputLeavesTheHost(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*usecases.LogsCommand, *chainStub)
		want   error
	}{
		{
			name:   "another shard's run",
			mutate: func(c *usecases.LogsCommand, s *chainStub) { s.record.ID = 8 },
			want:   shared.ErrValidation,
		},
		{
			name:   "shard the chain has forgotten",
			mutate: func(_ *usecases.LogsCommand, s *chainStub) { s.found = false },
			want:   shared.ErrNotFound,
		},
		{
			name:   "shard already settled",
			mutate: func(_ *usecases.LogsCommand, s *chainStub) { s.record.Status = shard.StatusSettled },
			want:   shared.ErrConflict,
		},
		{
			name:   "node this host holds but the shard does not",
			mutate: func(c *usecases.LogsCommand, _ *chainStub) { c.Node = nodeB },
			want:   shared.ErrConflict,
		},
		{
			name:   "someone who is not the run's creator",
			mutate: func(c *usecases.LogsCommand, _ *chainStub) { c.Actor = shard.Actor{Address: "gonka1stranger"} },
			want:   shared.ErrForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			chain, streams := newChainStub(), &streamsStub{output: "secret"}
			cmd := logsCommand()
			tc.mutate(&cmd, chain)
			var out bytes.Buffer

			err := usecases.NewStreamLogsUseCase(chain, streams).Execute(context.Background(), cmd, &out)

			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if streams.logs != nil || out.Len() != 0 {
				t.Fatalf("nothing may be read from the container: %q", out.String())
			}
		})
	}
}

func TestShellIsAuthorizedTheSameWayAndOpensInsideTheRun(t *testing.T) {

	chain, streams, log := newChainStub(), &streamsStub{}, newSessionLogStub()
	session := strings.NewReader("whoami\n")
	var out bytes.Buffer
	shell := usecases.NewOpenShellUseCase(chain, streams, log, timex.NewFrozen(since))

	err := shell.Execute(context.Background(), logsCommand().SessionCommand, pipe{session, &out})

	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	if streams.exec.Shard != shardID || streams.exec.Node != nodeA {
		t.Fatalf("got %+v, want the run's own container", *streams.exec)
	}

	stranger := logsCommand().SessionCommand
	stranger.Actor = shard.Actor{Address: "gonka1stranger"}
	streams.exec = nil

	err = shell.Execute(context.Background(), stranger, pipe{session, &out})

	if !errors.Is(err, shared.ErrForbidden) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if streams.exec != nil {
		t.Fatal("a stranger must never reach the container")
	}
}

func TestTheHostKeepsWhatCrossedAShell(t *testing.T) {

	chain, streams, log := newChainStub(), &streamsStub{}, newSessionLogStub()
	session := strings.NewReader("whoami\n")
	var out bytes.Buffer

	err := usecases.NewOpenShellUseCase(chain, streams, log, timex.NewFrozen(since)).
		Execute(context.Background(), logsCommand().SessionCommand, pipe{session, &out})

	if err != nil {
		t.Fatalf("open shell: %v", err)
	}
	if log.kept.String() != "whoami\nwhoami\n" {
		t.Fatalf("got %q, want both directions in the order they happened", log.kept.String())
	}
	if !log.closed {
		t.Fatal("the record must be closed when the session ends")
	}
}

func TestAShellThatCannotBeRecordedIsNotOpened(t *testing.T) {

	chain, streams, log := newChainStub(), &streamsStub{}, newSessionLogStub()
	log.err = errors.New("no room for the record")
	var out bytes.Buffer

	err := usecases.NewOpenShellUseCase(chain, streams, log, timex.NewFrozen(since)).
		Execute(context.Background(), logsCommand().SessionCommand, pipe{strings.NewReader("whoami\n"), &out})

	if !errors.Is(err, log.err) {
		t.Fatalf("got %v, want the reason the record could not be opened", err)
	}
	if streams.exec != nil {
		t.Fatal("an unrecorded session must never reach the container")
	}
}
