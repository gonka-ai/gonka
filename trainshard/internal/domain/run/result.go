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

// PerHost asks every host at once and answers grouped by host, hosts in the order their
// first node was named, never in the order the hosts happened to finish; every node asked
// about gets an answer, so a host cannot drop one and have the silence read as agreement
func PerHost[T Answer](
	ctx context.Context,
	nodes []vo.NodeRef,
	failed func(vo.NodeRef, error) T,
	call func(context.Context, vo.Participant, []vo.NodeRef) ([]T, error),
) []T {
	order, held := byParticipant(nodes)

	answers := syncx.Fan(order, func(participant vo.Participant) []T {
		answered, err := call(ctx, participant, held[participant])
		if err != nil {
			answered = nil
		}
		return append(answered, unanswered(held[participant], answered, failed, err)...)
	})

	results := make([]T, 0, len(nodes))
	for _, answered := range answers {
		results = append(results, answered...)
	}
	return results
}

func unanswered[T Answer](asked []vo.NodeRef, answered []T, failed func(vo.NodeRef, error) T, cause error) []T {
	if cause == nil {
		cause = ErrNodeNotAnswered
	}
	named := make(map[vo.NodeRef]bool, len(answered))
	for _, answer := range answered {
		named[answer.Ref()] = true
	}

	gaps := make([]T, 0)
	for _, node := range asked {
		if !named[node] {
			gaps = append(gaps, failed(node, cause))
		}
	}
	return gaps
}

func byParticipant(nodes []vo.NodeRef) ([]vo.Participant, map[vo.Participant][]vo.NodeRef) {
	order := make([]vo.Participant, 0, len(nodes))
	held := make(map[vo.Participant][]vo.NodeRef, len(nodes))

	for _, node := range nodes {
		if _, seen := held[node.Participant]; !seen {
			order = append(order, node.Participant)
		}
		held[node.Participant] = append(held[node.Participant], node)
	}
	return order, held
}

type NodeStatus struct {
	NodeResult
	Prepared       bool
	MeshUp         bool
	GPUsInUse      int
	DiskBytes      int64
	DiskQuotaBytes int64
}

func FailedStatus(node vo.NodeRef, err error) NodeStatus {
	return NodeStatus{NodeResult: Failed(node, err)}
}

// AgreedImage is the one image the whole run holds, and errors unless every node answered:
// a node we could not read may hold anything, so its silence cannot pass for agreement
func AgreedImage(statuses []NodeStatus) (vo.ImageDigest, error) {
	held := make([]NodeImage, 0, len(statuses))
	for _, status := range statuses {
		if !status.OK() {
			return "", ErrStatusUnknown
		}
		if status.State.Exists() {
			held = append(held, NodeImage{Node: status.Node, Image: status.Image})
		}
	}
	if len(held) == 0 {
		return "", nil
	}
	return SameImage(held)
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

func ReportOf(node vo.NodeRef, state RunState, observed Observed) NodeReport {
	images := state.Images
	if images == nil {
		images = make([]ImageRun, 0)
	}
	return NodeReport{Node: node, Images: images, ExitCode: observed.ExitCode, Fault: state.Fault}
}

type RunState struct {
	Shard      vo.ShardID
	ReservedAt time.Time
	Spec       RunSpec
	Start      bool
	StopGrace  time.Duration
	Images     []ImageRun
	Fault      *shared.Fault
	FaultAt    time.Time
}
