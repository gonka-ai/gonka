package run

import (
	"context"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/syncx"
)

type NodeImage struct {
	Node  vo.NodeRef
	Image vo.ImageDigest
}

type NodeResult struct {
	Node     vo.NodeRef
	State    vo.ContainerState
	Image    vo.ImageDigest
	ExitCode *int
	Fault    *shared.Fault
}

func Failed(node vo.NodeRef, err error) NodeResult {
	return NodeResult{Node: node, State: vo.ContainerUnknown, Fault: shared.NewFault(err)}
}

func ResultOf(node vo.NodeRef, container ContainerInfo) NodeResult {
	return NodeResult{Node: node, State: container.State, Image: container.Image, ExitCode: container.ExitCode}
}

func (r NodeResult) OK() bool { return r.Fault == nil }

func (r NodeResult) Ref() vo.NodeRef { return r.Node }

// Answer is whatever a host says about one node, so a batch can be matched back to what was asked
type Answer interface{ Ref() vo.NodeRef }

func PerNode[T any](nodes []vo.NodeRef, failed func(vo.NodeRef, error) T, run func(vo.NodeRef) (T, error)) []T {
	results := make([]T, 0, len(nodes))
	for _, node := range nodes {
		result, err := run(node)
		if err != nil {
			result = failed(node, err)
		}
		results = append(results, result)
	}
	return results
}

// PerHost asks every host at once and answers grouped by host, in the order the hosts were
// given, never in the order they happened to finish; every node a host serves gets an answer, so a
// host cannot drop one and have the silence read as agreement
func PerHost[T Answer](
	ctx context.Context,
	hosts []vo.Host,
	failed func(vo.NodeRef, error) T,
	call func(context.Context, vo.Host) ([]T, error),
) []T {
	answers := syncx.Fan(hosts, func(host vo.Host) []T {
		answered, err := call(ctx, host)
		if err != nil {
			answered = nil
		}
		return matched(host.Nodes, answered, failed, err)
	})

	results := make([]T, 0, len(hosts))
	for _, answered := range answers {
		results = append(results, answered...)
	}
	return results
}

// matched lines the answers up with the nodes they were asked about, so a host can neither drop
// one of its nodes, nor answer twice for the same one, nor slip in an answer for a node that is
// not its own
func matched[T Answer](asked []vo.NodeRef, answered []T, failed func(vo.NodeRef, error) T, cause error) []T {
	if cause == nil {
		cause = ErrNodeNotAnswered
	}
	byNode := make(map[vo.NodeRef]T, len(answered))
	for _, answer := range answered {
		if _, twice := byNode[answer.Ref()]; twice {
			byNode[answer.Ref()] = failed(answer.Ref(), ErrNodeAnsweredTwice)
			continue
		}
		byNode[answer.Ref()] = answer
	}

	results := make([]T, 0, len(asked))
	for _, node := range asked {
		answer, found := byNode[node]
		if !found {
			answer = failed(node, cause)
		}
		results = append(results, answer)
	}
	return results
}

type NodeStatus struct {
	NodeResult
	Prepared       bool
	Waiting        string
	MeshUp         bool
	GPUsInUse      int
	DiskBytes      int64
	DiskQuotaBytes int64
}

func FailedStatus(node vo.NodeRef, err error) NodeStatus {
	return NodeStatus{NodeResult: Failed(node, err)}
}

// ReadyToStart holds when every node answered, is still prepared and on the mesh, can still
// start its container, and they all hold the same image; a run started on only some of its
// nodes waits for the rest with the gpus already taken
func ReadyToStart(statuses []NodeStatus) error {
	held := make([]NodeImage, 0, len(statuses))
	for _, status := range statuses {
		if !status.OK() {
			return ErrStatusUnknown
		}
		if !status.Prepared {
			return ErrNodeNotPrepared
		}
		if !status.MeshUp {
			return ErrMeshDown
		}
		if err := CanStart(status.State); err != nil {
			return err
		}
		held = append(held, NodeImage{Node: status.Node, Image: status.Image})
	}
	_, err := SameImage(held)
	return err
}

func StatusOf(node vo.NodeRef, desired Desired, observed Observed, fault *shared.Fault) NodeStatus {
	return NodeStatus{
		NodeResult: NodeResult{
			Node:     node,
			State:    observed.Container,
			Image:    observed.ContainerImage,
			ExitCode: observed.ExitCode,
			Fault:    fault,
		},
		Prepared:       Prepared(desired, observed),
		Waiting:        Unprepared(desired, observed),
		MeshUp:         observed.MeshUp,
		GPUsInUse:      observed.GPUsInUse,
		DiskBytes:      observed.DiskUsedBytes,
		DiskQuotaBytes: observed.DiskQuotaBytes,
	}
}

type ImageRun struct {
	Image vo.ImageDigest
	At    time.Time
}

type NodeReport struct {
	Node     vo.NodeRef
	Images   []ImageRun
	ExitCode *int
	Fault    *shared.Fault
}

func (r NodeReport) Ref() vo.NodeRef { return r.Node }

func FailedReport(node vo.NodeRef, err error) NodeReport {
	return NodeReport{Node: node, Images: make([]ImageRun, 0), Fault: shared.NewFault(err)}
}

func ReportOf(node vo.NodeRef, state RunState, exitCode *int) NodeReport {
	images := state.Images
	if images == nil {
		images = make([]ImageRun, 0)
	}
	return NodeReport{Node: node, Images: images, ExitCode: exitCode, Fault: state.Fault}
}

type RunState struct {
	Shard        vo.ShardID
	ReservedAt   time.Time
	Spec         RunSpec
	Revision     int
	Start        bool
	StopGrace    time.Duration
	Images       []ImageRun
	Fault        *shared.Fault
	FaultAt      time.Time
	UnpreparedAt time.Time
	ReleasedAt   time.Time
}
