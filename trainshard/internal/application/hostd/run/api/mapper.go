package api

import (
	"encoding/hex"
	"fmt"
	"time"

	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/contract"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

var (
	errShardMismatch = shared.New("SHARD_MISMATCH", shared.ErrValidation, "path and body name different shards")
	errNoNodes       = shared.New("NO_NODES", shared.ErrValidation, "no node ids in the request")
	errMeshRanks     = shared.New("MESH_RANKS", shared.ErrValidation, "peer ranks do not match the agreed ordering")
	errGrace         = shared.New("BAD_GRACE", shared.ErrValidation, "grace period cannot be negative")
)

func toNodesCommand(participant vo.Participant, actor shard.Actor, path string, dto contract.Command) (usecases.NodesCommand, error) {
	shardID, err := vo.ParseShardID(path)
	if err != nil {
		return usecases.NodesCommand{}, err
	}
	if dto.ShardID != "" && dto.ShardID != path {
		return usecases.NodesCommand{}, errShardMismatch
	}

	nodes, err := toNodeRefs(participant, dto.NodeIDs)
	if err != nil {
		return usecases.NodesCommand{}, err
	}
	requestID, err := vo.ParseRequestID(dto.RequestID)
	if err != nil {
		return usecases.NodesCommand{}, err
	}
	deadline, err := time.Parse(time.RFC3339, dto.Deadline)
	if err != nil {
		return usecases.NodesCommand{}, fmt.Errorf("deadline %q: %w", dto.Deadline, shared.ErrValidation)
	}

	return usecases.NodesCommand{
		Shard:     shardID,
		Nodes:     nodes,
		Actor:     actor,
		RequestID: requestID,
		Deadline:  deadline,
	}, nil
}

func toDeployCommand(participant vo.Participant, actor shard.Actor, path string, dto contract.DeployRequest) (usecases.DeployCommand, error) {
	base, err := toNodesCommand(participant, actor, path, dto.Command)
	if err != nil {
		return usecases.DeployCommand{}, err
	}
	digest, err := vo.ParseImageDigest(dto.ImageDigest)
	if err != nil {
		return usecases.DeployCommand{}, err
	}
	sources, err := toSources(dto.Sources)
	if err != nil {
		return usecases.DeployCommand{}, err
	}

	return usecases.DeployCommand{
		NodesCommand: base,
		Run: run.RunSpec{
			Image:     digest,
			Command:   dto.Args,
			Env:       dto.Env,
			Sources:   sources,
			Resources: run.Resources{GPUs: dto.GPUs, DiskBytes: dto.DiskBytes},
		},
	}, nil
}

func toSources(declared []string) ([]vo.Source, error) {
	seen := make(map[vo.Source]struct{}, len(declared))
	sources := make([]vo.Source, 0, len(declared))
	for _, entry := range declared {
		source, err := vo.ParseSource(entry)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[source]; duplicate {
			continue
		}
		seen[source] = struct{}{}
		sources = append(sources, source)
	}
	return sources, nil
}

func toStopCommand(participant vo.Participant, actor shard.Actor, path string, dto contract.StopRequest) (usecases.StopCommand, error) {
	base, err := toNodesCommand(participant, actor, path, dto.Command)
	if err != nil {
		return usecases.StopCommand{}, err
	}
	if dto.GraceSeconds < 0 {
		return usecases.StopCommand{}, errGrace
	}
	return usecases.StopCommand{NodesCommand: base, Grace: time.Duration(dto.GraceSeconds) * time.Second}, nil
}

func toMeshCommand(participant vo.Participant, actor shard.Actor, path string, dto contract.MeshRequest) (usecases.MeshCommand, error) {
	base, err := toNodesCommand(participant, actor, path, dto.Command)
	if err != nil {
		return usecases.MeshCommand{}, err
	}

	members := make([]mesh.Member, 0, len(dto.Peers))
	for _, peer := range dto.Peers {
		ref, err := vo.ParseNodeRef(peer.Participant, peer.NodeID)
		if err != nil {
			return usecases.MeshCommand{}, err
		}
		members = append(members, mesh.Member{Node: ref, Address: peer.Address, PublicKey: peer.PublicKey})
	}

	config, err := mesh.Order(base.Shard, members)
	if err != nil {
		return usecases.MeshCommand{}, err
	}
	if err := sameRanks(config, dto.Peers); err != nil {
		return usecases.MeshCommand{}, err
	}
	return usecases.MeshCommand{NodesCommand: base, Config: config}, nil
}

func sameRanks(config mesh.Config, peers []contract.Peer) error {
	ranks := make(map[vo.NodeRef]int, len(config.Peers))
	for _, peer := range config.Peers {
		ranks[peer.Node] = peer.Rank
	}
	for _, peer := range peers {
		ref := vo.NodeRef{Participant: vo.Participant(peer.Participant), NodeID: vo.NodeID(peer.NodeID)}
		if rank, found := ranks[ref]; !found || rank != peer.Rank {
			return errMeshRanks
		}
	}
	return nil
}

func toNodeRefs(participant vo.Participant, ids []string) ([]vo.NodeRef, error) {
	seen := make(map[vo.NodeRef]struct{}, len(ids))
	nodes := make([]vo.NodeRef, 0, len(ids))
	for _, id := range ids {
		ref, err := vo.ParseNodeRef(string(participant), id)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		nodes = append(nodes, ref)
	}
	if len(nodes) == 0 {
		return nil, errNoNodes
	}
	return nodes, nil
}

func toNodePath(participant vo.Participant, path, nodeID string) (vo.ShardID, vo.NodeRef, error) {
	shardID, err := vo.ParseShardID(path)
	if err != nil {
		return 0, vo.NodeRef{}, err
	}
	node, err := vo.ParseNodeRef(string(participant), nodeID)
	if err != nil {
		return 0, vo.NodeRef{}, err
	}
	return shardID, node, nil
}

func toProbeOutput(node vo.NodeRef, unreachable []vo.NodeRef) contract.ProbeResult {
	peers := make([]contract.PeerRef, 0, len(unreachable))
	for _, peer := range unreachable {
		peers = append(peers, contract.PeerRef{Participant: string(peer.Participant), NodeID: string(peer.NodeID)})
	}
	return contract.ProbeResult{NodeID: string(node.NodeID), Unreachable: peers}
}

func toMeshOutput(identities []mesh.Identity) contract.MeshResult {
	items := make([]contract.MeshIdentity, 0, len(identities))
	for _, identity := range identities {
		items = append(items, contract.MeshIdentity{
			NodeID:    string(identity.Member.Node.NodeID),
			Address:   identity.Member.Address,
			PublicKey: identity.Member.PublicKey,
			Signature: hex.EncodeToString(identity.Signature),
		})
	}
	return contract.MeshResult{Items: items}
}

func toNodesOutput(results []run.NodeResult) contract.NodesResult {
	items := make([]contract.NodeResult, 0, len(results))
	for _, result := range results {
		items = append(items, toNodeResult(result))
	}
	return contract.NodesResult{Items: items}
}

func toStatusOutput(statuses []run.NodeStatus) contract.StatusResult {
	items := make([]contract.NodeStatus, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, contract.NodeStatus{
			NodeResult:     toNodeResult(status.NodeResult),
			Prepared:       status.Prepared,
			MeshUp:         status.MeshUp,
			GPUsInUse:      status.GPUsInUse,
			DiskBytes:      status.DiskBytes,
			DiskQuotaBytes: status.DiskQuotaBytes,
		})
	}
	return contract.StatusResult{Items: items}
}

func toReportOutput(reports []run.NodeReport) contract.ReportResult {
	items := make([]contract.NodeReport, 0, len(reports))
	for _, report := range reports {
		images := make([]contract.ImageRun, 0, len(report.Images))
		for _, image := range report.Images {
			images = append(images, contract.ImageRun{
				ImageDigest: image.Image.String(),
				At:          image.At.UTC().Format(time.RFC3339),
			})
		}
		items = append(items, contract.NodeReport{
			NodeID:   string(report.Node.NodeID),
			Images:   images,
			ExitCode: report.ExitCode,
			Error:    toError(report.Fault),
		})
	}
	return contract.ReportResult{Items: items}
}

func toNodeResult(result run.NodeResult) contract.NodeResult {
	return contract.NodeResult{
		NodeID:      string(result.Node.NodeID),
		State:       string(result.State),
		ImageDigest: result.Image.String(),
		ExitCode:    result.ExitCode,
		Error:       toError(result.Fault),
	}
}

func toError(fault *shared.Fault) *contract.Error {
	if fault == nil {
		return nil
	}
	return &contract.Error{Code: fault.Code, Message: fault.Reason}
}
