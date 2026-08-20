package hosts

import (
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"trainshard/internal/contract"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func toPath(pattern string, shardID vo.ShardID, node vo.NodeID) string {
	return strings.NewReplacer("{shard_id}", shardID.String(), "{node_id}", string(node)).Replace(pattern)
}

func fromCommand(call run.HostCommand) contract.Command {
	nodes := make([]string, 0, len(call.Nodes))
	for _, node := range call.Nodes {
		nodes = append(nodes, string(node.NodeID))
	}
	return contract.Command{
		ShardID:   call.Shard.String(),
		NodeIDs:   nodes,
		RequestID: string(call.RequestID),
		Deadline:  call.Deadline.UTC().Format(time.RFC3339),
	}
}

func fromDeploy(call run.DeployCall) contract.DeployRequest {
	sources := make([]string, 0, len(call.Run.Sources))
	for _, source := range call.Run.Sources {
		sources = append(sources, source.String())
	}

	return contract.DeployRequest{
		Command:     fromCommand(call.HostCommand),
		ImageDigest: call.Run.Image.String(),
		Args:        call.Run.Command,
		Env:         call.Run.Env,
		Sources:     sources,
		GPUs:        call.Run.Resources.GPUs,
		DiskBytes:   call.Run.Resources.DiskBytes,
	}
}

func toNodeResults(participant vo.Participant, items []contract.NodeResult) []run.NodeResult {
	results := make([]run.NodeResult, 0, len(items))
	for _, item := range items {
		results = append(results, toNodeResult(participant, item))
	}
	return results
}

func toNodeResult(participant vo.Participant, item contract.NodeResult) run.NodeResult {
	return run.NodeResult{
		Node:     vo.NodeRef{Participant: participant, NodeID: vo.NodeID(item.NodeID)},
		State:    vo.ContainerState(item.State),
		Image:    vo.ImageDigest(item.ImageDigest),
		ExitCode: item.ExitCode,
		Fault:    toFault(item.Error),
	}
}

func toNodeStatuses(participant vo.Participant, items []contract.NodeStatus) []run.NodeStatus {
	statuses := make([]run.NodeStatus, 0, len(items))
	for _, item := range items {
		statuses = append(statuses, run.NodeStatus{
			NodeResult:     toNodeResult(participant, item.NodeResult),
			Prepared:       item.Prepared,
			MeshUp:         item.MeshUp,
			GPUsInUse:      item.GPUsInUse,
			DiskBytes:      item.DiskBytes,
			DiskQuotaBytes: item.DiskQuotaBytes,
		})
	}
	return statuses
}

func toReports(participant vo.Participant, items []contract.NodeReport) ([]run.NodeReport, error) {
	reports := make([]run.NodeReport, 0, len(items))
	for _, item := range items {
		images := make([]run.ImageRun, 0, len(item.Images))
		for _, image := range item.Images {
			at, err := time.Parse(time.RFC3339, image.At)
			if err != nil {
				return nil, shared.New("BAD_REPORT", shared.ErrUnavailable, "host reported an image with an unreadable time")
			}
			images = append(images, run.ImageRun{Image: vo.ImageDigest(image.ImageDigest), At: at})
		}
		reports = append(reports, run.NodeReport{
			Node:     vo.NodeRef{Participant: participant, NodeID: vo.NodeID(item.NodeID)},
			Images:   images,
			ExitCode: item.ExitCode,
			Fault:    toFault(item.Error),
		})
	}
	return reports, nil
}

func toNodeError(items []contract.NodeResult) error {
	for _, item := range items {
		if item.Error != nil {
			return shared.New(item.Error.Code, shared.ErrUnavailable, item.Error.Message)
		}
	}
	return nil
}

func toFault(err *contract.Error) *shared.Fault {
	if err == nil {
		return nil
	}
	return &shared.Fault{Code: err.Code, Reason: err.Message}
}

func toIdentities(participant vo.Participant, items []contract.MeshIdentity) ([]mesh.Identity, error) {
	identities := make([]mesh.Identity, 0, len(items))
	for _, item := range items {
		signature, err := hex.DecodeString(item.Signature)
		if err != nil {
			return nil, mesh.ErrForeignIdentity
		}
		identities = append(identities, mesh.Identity{
			Member: mesh.Member{
				Node:      vo.NodeRef{Participant: participant, NodeID: vo.NodeID(item.NodeID)},
				Address:   item.Address,
				PublicKey: item.PublicKey,
			},
			Signature: signature,
		})
	}
	return identities, nil
}

func fromConfig(config mesh.Config, node vo.NodeRef, requestID vo.RequestID, deadline time.Time) contract.MeshRequest {
	peers := make([]contract.Peer, 0, len(config.Peers))
	for _, peer := range config.Peers {
		peers = append(peers, contract.Peer{
			Rank:        peer.Rank,
			Participant: string(peer.Node.Participant),
			NodeID:      string(peer.Node.NodeID),
			Address:     peer.Address,
			PublicKey:   peer.PublicKey,
		})
	}

	return contract.MeshRequest{
		Command: contract.Command{
			ShardID:   config.Shard.String(),
			NodeIDs:   []string{string(node.NodeID)},
			RequestID: string(requestID),
			Deadline:  deadline.UTC().Format(time.RFC3339),
		},
		Peers: peers,
	}
}

func toPairs(node vo.NodeRef, result contract.ProbeResult) []mesh.Pair {
	pairs := make([]mesh.Pair, 0, len(result.Unreachable))
	for _, peer := range result.Unreachable {
		other := vo.NodeRef{Participant: vo.Participant(peer.Participant), NodeID: vo.NodeID(peer.NodeID)}
		pairs = append(pairs, mesh.NewPair(node, other))
	}
	return pairs
}

func toError(status int, reported *contract.Error) error {
	kind := shared.ErrUnavailable
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = shared.ErrValidation
	case http.StatusUnauthorized:
		kind = shared.ErrUnauthorized
	case http.StatusForbidden:
		kind = shared.ErrForbidden
	case http.StatusNotFound:
		kind = shared.ErrNotFound
	case http.StatusConflict:
		kind = shared.ErrConflict
	}

	if reported == nil {
		return shared.New("HOST_ERROR", kind, "host answered "+http.StatusText(status))
	}
	return shared.New(reported.Code, kind, reported.Message)
}
