package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"trainshard/internal/application/coord/ops/cli"
	usecases "trainshard/internal/application/coord/ops/use_cases"
	"trainshard/internal/domain/run"
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
