package cli_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"trainshard/internal/application/coord/ops/cli"
	usecases "trainshard/internal/application/coord/ops/use_cases"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
)

func TestStatusShowsTheImageAndExitCodeOfEveryNode(t *testing.T) {
	// arrange
	nodeA := vo.NodeRef{Participant: "gonka1hosta", NodeID: "node-a"}
	nodeB := vo.NodeRef{Participant: "gonka1hostb", NodeID: "node-b"}
	runImage := vo.ImageDigest("run@sha256:" + strings.Repeat("a", 64))
	otherRun := vo.ImageDigest("other@sha256:" + strings.Repeat("b", 64))
	exitCode := 3
	hosts := hostsStub{statuses: map[vo.NodeRef]run.NodeStatus{
		nodeA: {NodeResult: run.NodeResult{Node: nodeA, State: vo.ContainerCreated, Image: runImage}},
		nodeB: {NodeResult: run.NodeResult{Node: nodeB, State: vo.ContainerExited, Image: otherRun, ExitCode: &exitCode}},
	}}
	chain := chainStub{nodes: []vo.NodeRef{nodeA, nodeB}}
	out := &bytes.Buffer{}
	commands := cli.New(cli.UseCases{Status: usecases.NewStatusUseCase(chain, hosts)},
		timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)), time.Minute, out, nil)

	// act
	err := commands.Status(context.Background(), []string{"7"})

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %q, want a header and one line per node", out.String())
	}
	if !strings.Contains(lines[1], runImage.String()) {
		t.Fatalf("got %q, want the image node-a holds", lines[1])
	}
	if !strings.Contains(lines[2], otherRun.String()) || strings.Fields(lines[2])[3] != "3" {
		t.Fatalf("got %q, want the image node-b holds and the code it exited with", lines[2])
	}
}

func TestStatusNamesThePeersANodeHasNotHeardFrom(t *testing.T) {
	// arrange
	nodeA := vo.NodeRef{Participant: "gonka1hosta", NodeID: "node-a"}
	nodeB := vo.NodeRef{Participant: "gonka1hostb", NodeID: "node-b"}
	hosts := hostsStub{statuses: map[vo.NodeRef]run.NodeStatus{
		nodeA: {NodeResult: run.NodeResult{Node: nodeA, State: vo.ContainerRunning}, MeshUp: true, MeshSilent: []vo.NodeRef{nodeB}},
	}}
	chain := chainStub{nodes: []vo.NodeRef{nodeA}}
	out := &bytes.Buffer{}
	commands := cli.New(cli.UseCases{Status: usecases.NewStatusUseCase(chain, hosts)},
		timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)), time.Minute, out, nil)

	// act
	err := commands.Status(context.Background(), []string{"7"})

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "NOT_HEARD") || !strings.Contains(lines[1], nodeB.String()) {
		t.Fatalf("got %q, want node-b named as not heard from", out.String())
	}
}

type stopHostsStub struct {
	call run.StopCall
}

func (*stopHostsStub) Deploy(context.Context, vo.Host, run.DeployCall) ([]run.NodeResult, error) {
	return nil, nil
}

func (*stopHostsStub) Start(context.Context, vo.Host, run.HostCommand) ([]run.NodeResult, error) {
	return nil, nil
}

func (h *stopHostsStub) Stop(_ context.Context, _ vo.Host, call run.StopCall) ([]run.NodeResult, error) {
	h.call = call
	results := make([]run.NodeResult, 0, len(call.Nodes))
	for _, node := range call.Nodes {
		results = append(results, run.NodeResult{Node: node, State: vo.ContainerExited})
	}
	return results, nil
}

func (*stopHostsStub) Status(context.Context, vo.Host, run.HostCommand) ([]run.NodeStatus, error) {
	return nil, nil
}

func TestStopLeavesGraceUnsetWhenFlagIsNotGiven(t *testing.T) {
	// arrange
	node := vo.NodeRef{Participant: "gonka1hosta", NodeID: "node-a"}
	hosts := &stopHostsStub{}
	out := &bytes.Buffer{}
	commands := cli.New(
		cli.UseCases{Stop: usecases.NewStopUseCase(chainStub{nodes: []vo.NodeRef{node}}, hosts)},
		timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)),
		time.Minute, out, nil,
	)

	// act
	err := commands.Stop(context.Background(), []string{"7"})

	// assert
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if hosts.call.GraceGiven || hosts.call.Grace != 30*time.Second {
		t.Fatalf("got %+v, want grace left not given", hosts.call)
	}
}

func TestStopKeepsAnExplicitZeroGraceFlag(t *testing.T) {
	// arrange
	node := vo.NodeRef{Participant: "gonka1hosta", NodeID: "node-a"}
	hosts := &stopHostsStub{}
	out := &bytes.Buffer{}
	commands := cli.New(
		cli.UseCases{Stop: usecases.NewStopUseCase(chainStub{nodes: []vo.NodeRef{node}}, hosts)},
		timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)),
		time.Minute, out, nil,
	)

	// act
	err := commands.Stop(context.Background(), []string{"7", "-grace", "0"})

	// assert
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !hosts.call.GraceGiven || hosts.call.Grace != 0 {
		t.Fatalf("got %+v, want explicit zero grace", hosts.call)
	}
}

func TestStopRefusesANegativeGraceBeforeAnyHostIsAsked(t *testing.T) {
	// arrange
	node := vo.NodeRef{Participant: "gonka1hosta", NodeID: "node-a"}
	hosts := &stopHostsStub{}
	commands := cli.New(
		cli.UseCases{Stop: usecases.NewStopUseCase(chainStub{nodes: []vo.NodeRef{node}}, hosts)},
		timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)),
		time.Minute, &bytes.Buffer{}, nil,
	)

	// act
	err := commands.Stop(context.Background(), []string{"7", "-grace", "-500ms"})

	// assert
	if !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("got %v, want a validation error", err)
	}
	if hosts.call.Nodes != nil {
		t.Fatalf("got %+v, want no host asked", hosts.call)
	}
}
