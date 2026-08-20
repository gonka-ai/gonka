package usecases_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	usecases "trainshard/internal/application/coord/ops/use_cases"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

const shardID = vo.ShardID(7)

var (
	hostA     = vo.Participant("gonka1hosta")
	hostB     = vo.Participant("gonka1hostb")
	nodeA     = vo.NodeRef{Participant: hostA, NodeID: "node-a"}
	nodeB     = vo.NodeRef{Participant: hostB, NodeID: "node-b"}
	runImage  = vo.ImageDigest("run@sha256:" + strings.Repeat("a", 64))
	otherRun  = vo.ImageDigest("other@sha256:" + strings.Repeat("b", 64))
	baseImage = vo.ImageDigest("base@sha256:" + strings.Repeat("c", 64))
)

type chainStub struct{}

func (chainStub) Height(context.Context) (vo.Height, error) { return 500, nil }

func (chainStub) Shard(_ context.Context, id vo.ShardID) (shard.Shard, bool, error) {
	return shard.Shard{
		ID:              id,
		Status:          shard.StatusActive,
		BaseImage:       baseImage,
		ExpiresAtHeight: 1000,
		Nodes:           []shard.ReservedNode{{Ref: nodeA}, {Ref: nodeB}},
	}, true, nil
}

func (chainStub) Reservation(context.Context, vo.NodeRef) (vo.ShardID, bool, error) {
	return shardID, true, nil
}

func (chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{}, nil
}

func (chainStub) ActiveShards(context.Context) ([]shard.Shard, error) { return nil, nil }

type hostsStub struct {
	mu      sync.Mutex
	images  map[vo.NodeRef]vo.ImageDigest
	started []vo.NodeRef
}

func (h *hostsStub) Deploy(context.Context, vo.Participant, run.DeployCall) ([]run.NodeResult, error) {
	return nil, nil
}

func (h *hostsStub) Stop(context.Context, vo.Participant, run.StopCall) ([]run.NodeResult, error) {
	return nil, nil
}

func (h *hostsStub) Status(_ context.Context, _ vo.Participant, call run.HostCommand) ([]run.NodeStatus, error) {
	statuses := make([]run.NodeStatus, 0, len(call.Nodes))
	for _, node := range call.Nodes {
		held, found := h.images[node]
		state := vo.ContainerAbsent
		if found {
			state = vo.ContainerCreated
		}
		statuses = append(statuses, run.NodeStatus{
			NodeResult: run.NodeResult{Node: node, State: state, Image: held},
		})
	}
	return statuses, nil
}

func (h *hostsStub) Start(_ context.Context, _ vo.Participant, call run.HostCommand) ([]run.NodeResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	results := make([]run.NodeResult, 0, len(call.Nodes))
	for _, node := range call.Nodes {
		h.started = append(h.started, node)
		results = append(results, run.NodeResult{Node: node, State: vo.ContainerRunning, Image: h.images[node]})
	}
	return results, nil
}

func runCommand() usecases.RunCommand {
	return usecases.RunCommand{Shard: shardID, RequestID: "req-1", Deadline: time.Now().Add(time.Minute)}
}

func TestStartRefusesTheWholeRunWhenHostsHoldDifferentImages(t *testing.T) {

	hosts := &hostsStub{images: map[vo.NodeRef]vo.ImageDigest{nodeA: runImage, nodeB: otherRun}}

	_, err := usecases.NewStartUseCase(chainStub{}, hosts).Execute(context.Background(), runCommand())

	if !errors.Is(err, run.ErrImagesDiffer) {
		t.Fatalf("got %v, want %v", err, run.ErrImagesDiffer)
	}
	if len(hosts.started) != 0 {
		t.Fatalf("a refused run must start nothing, got %v", hosts.started)
	}
}

func TestStartAcceptsARunWhoseHostsAgreeOnTheImage(t *testing.T) {

	hosts := &hostsStub{images: map[vo.NodeRef]vo.ImageDigest{nodeA: runImage, nodeB: runImage}}

	results, err := usecases.NewStartUseCase(chainStub{}, hosts).Execute(context.Background(), runCommand())

	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(results) != 2 || len(hosts.started) != 2 {
		t.Fatalf("got %+v started %v, want both nodes started", results, hosts.started)
	}
}

func TestStartLeavesAnEmptyRunToTheHostsToRefusePerNode(t *testing.T) {

	hosts := &hostsStub{images: map[vo.NodeRef]vo.ImageDigest{}}

	_, err := usecases.NewStartUseCase(chainStub{}, hosts).Execute(context.Background(), runCommand())

	if err != nil {
		t.Fatalf("a run with no containers is not a whole-request failure: %v", err)
	}
}
