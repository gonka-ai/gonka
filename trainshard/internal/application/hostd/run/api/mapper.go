package api

import (
	"encoding/hex"
	"fmt"
	"strings"
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
	errDeadlineFar   = shared.New("DEADLINE_TOO_FAR", shared.ErrValidation, "deadline is later than this daemon keeps a request's answer")
	errEnvName       = shared.New("BAD_ENV_NAME", shared.ErrValidation, "env names are letters, digits and underscores")
	errEnvValue      = shared.New("BAD_ENV_VALUE", shared.ErrValidation, "env values cannot hold a NUL byte")
)

// latest is the furthest deadline taken: a retry with the same request id past the request log's
// memory would run again
func toNodesCommand(host vo.Host, actor shard.Actor, path string, dto contract.Command, latest time.Time) (usecases.NodesCommand, error) {
	shardID, err := vo.ParseShardID(path)
	if err != nil {
		return usecases.NodesCommand{}, err
	}
	if dto.ShardID != "" && dto.ShardID != path {
		return usecases.NodesCommand{}, errShardMismatch
	}

	nodes, err := toNodeRefs(host, dto.NodeIDs)
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
	if deadline.After(latest) {
		return usecases.NodesCommand{}, errDeadlineFar
	}

	return usecases.NodesCommand{
		Shard:     shardID,
		Nodes:     nodes,
		Actor:     actor,
		RequestID: requestID,
		Deadline:  deadline,
	}, nil
}

func toDeployCommand(host vo.Host, actor shard.Actor, path string, dto contract.DeployRequest, latest time.Time) (usecases.DeployCommand, error) {
	base, err := toNodesCommand(host, actor, path, dto.Command, latest)
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
	if err := checkEnv(dto.Env); err != nil {
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

// a name is cut at its first '=' when the container reads it, so a name holding one would set a
// variable the host-owned check never saw
func checkEnv(env map[string]string) error {
	for name, value := range env {
		if name == "" {
			return errEnvName
		}
		for i := 0; i < len(name); i++ {
			c := name[i]
			if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
				return errEnvName
			}
		}
		if strings.IndexByte(value, 0) >= 0 {
			return errEnvValue
		}
	}
	return nil
}

func toStopCommand(host vo.Host, actor shard.Actor, path string, dto contract.StopRequest, latest time.Time) (usecases.StopCommand, error) {
	base, err := toNodesCommand(host, actor, path, dto.Command, latest)
	if err != nil {
		return usecases.StopCommand{}, err
	}
	command := usecases.StopCommand{NodesCommand: base}
	if dto.GraceSeconds == nil {
		return command, nil
	}
	if *dto.GraceSeconds < 0 {
		return usecases.StopCommand{}, errGrace
	}
	command.Grace = time.Duration(*dto.GraceSeconds) * time.Second
	command.GraceGiven = true
	return command, nil
}

func toMeshCommand(host vo.Host, actor shard.Actor, path string, dto contract.MeshRequest, latest time.Time) (usecases.MeshCommand, error) {
	base, err := toNodesCommand(host, actor, path, dto.Command, latest)
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

func toNodeRefs(host vo.Host, ids []string) ([]vo.NodeRef, error) {
	seen := make(map[vo.NodeRef]struct{}, len(ids))
	nodes := make([]vo.NodeRef, 0, len(ids))
	for _, id := range ids {
		ref, err := host.Node(id)
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

func toNodePath(host vo.Host, path, nodeID string) (vo.ShardID, vo.NodeRef, error) {
	shardID, err := vo.ParseShardID(path)
	if err != nil {
		return 0, vo.NodeRef{}, err
	}
	node, err := host.Node(nodeID)
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
		silent := make([]string, 0, len(status.MeshSilent))
		for _, peer := range status.MeshSilent {
			silent = append(silent, peer.String())
		}
		items = append(items, contract.NodeStatus{
			NodeResult:     toNodeResult(status.NodeResult),
			Prepared:       status.Prepared,
			Waiting:        status.Waiting,
			MeshUp:         status.MeshUp,
			MeshSilent:     silent,
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
			Answered: report.Answered,
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
