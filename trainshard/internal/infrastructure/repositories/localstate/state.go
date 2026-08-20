package localstate

import (
	"time"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type nodeFile struct {
	ShardID    uint64    `json:"shard_id"`
	ReservedAt time.Time `json:"reserved_at,omitzero"`
	FaultAt    time.Time `json:"fault_at,omitzero"`

	Image     string     `json:"image_digest,omitempty"`
	Command   []string   `json:"command,omitempty"`
	Env       []envVar   `json:"env,omitempty"`
	Sources   []string   `json:"sources,omitempty"`
	GPUs      int        `json:"gpus,omitempty"`
	DiskBytes int64      `json:"disk_bytes,omitempty"`
	Start     bool       `json:"start"`
	StopGrace int        `json:"stop_grace_seconds,omitempty"`
	Images    []imageRun `json:"images,omitempty"`
	Fault     *fault     `json:"fault,omitempty"`
	Mesh      *meshState `json:"mesh,omitempty"`
}

type envVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type imageRun struct {
	Image string    `json:"image_digest"`
	At    time.Time `json:"at"`
}

type fault struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

type requestEntry struct {
	Results []nodeResult `json:"results"`
	At      time.Time    `json:"at"`
}

type nodeResult struct {
	Participant string `json:"participant"`
	NodeID      string `json:"node_id"`
	State       string `json:"state"`
	Image       string `json:"image_digest,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Fault       *fault `json:"fault,omitempty"`
}

type meshState struct {
	Address   string `json:"address,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Signature []byte `json:"signature,omitempty"`
	Peers     []peer `json:"peers,omitempty"`
}

type peer struct {
	Rank        int    `json:"rank"`
	Participant string `json:"participant"`
	NodeID      string `json:"node_id"`
	Address     string `json:"address"`
	PublicKey   string `json:"public_key"`
}

func toRunState(file nodeFile) (run.RunState, error) {
	state := run.RunState{
		Shard:      vo.ShardID(file.ShardID),
		ReservedAt: file.ReservedAt,
		FaultAt:    file.FaultAt,
		Spec: run.RunSpec{
			Image:     vo.ImageDigest(file.Image),
			Command:   file.Command,
			Resources: run.Resources{GPUs: file.GPUs, DiskBytes: file.DiskBytes},
		},
		Start:     file.Start,
		StopGrace: time.Duration(file.StopGrace) * time.Second,
	}

	if len(file.Env) > 0 {
		state.Spec.Env = make(map[string]string, len(file.Env))
		for _, entry := range file.Env {
			state.Spec.Env[entry.Name] = entry.Value
		}
	}
	for _, entry := range file.Sources {
		source, err := vo.ParseSource(entry)
		if err != nil {
			return run.RunState{}, err
		}
		state.Spec.Sources = append(state.Spec.Sources, source)
	}
	for _, image := range file.Images {
		state.Images = append(state.Images, run.ImageRun{Image: vo.ImageDigest(image.Image), At: image.At})
	}
	if file.Fault != nil {
		state.Fault = &shared.Fault{Code: file.Fault.Code, Reason: file.Fault.Reason}
	}
	return state, nil
}

func fromRunState(state run.RunState, keep *meshState) nodeFile {
	file := nodeFile{
		ShardID:    uint64(state.Shard),
		ReservedAt: state.ReservedAt,
		FaultAt:    state.FaultAt,

		Image:     state.Spec.Image.String(),
		Command:   state.Spec.Command,
		GPUs:      state.Spec.Resources.GPUs,
		DiskBytes: state.Spec.Resources.DiskBytes,
		Start:     state.Start,
		StopGrace: int(state.StopGrace.Seconds()),
		Mesh:      keep,
	}

	for name, value := range state.Spec.Env {
		file.Env = append(file.Env, envVar{Name: name, Value: value})
	}
	for _, source := range state.Spec.Sources {
		file.Sources = append(file.Sources, source.String())
	}
	for _, image := range state.Images {
		file.Images = append(file.Images, imageRun{Image: image.Image.String(), At: image.At})
	}
	if state.Fault != nil {
		file.Fault = &fault{Code: state.Fault.Code, Reason: state.Fault.Reason}
	}
	return file
}

func toNodeResults(entries []nodeResult) []run.NodeResult {
	results := make([]run.NodeResult, 0, len(entries))
	for _, entry := range entries {
		result := run.NodeResult{
			Node:     vo.NodeRef{Participant: vo.Participant(entry.Participant), NodeID: vo.NodeID(entry.NodeID)},
			State:    vo.ContainerState(entry.State),
			Image:    vo.ImageDigest(entry.Image),
			ExitCode: entry.ExitCode,
		}
		if entry.Fault != nil {
			result.Fault = &shared.Fault{Code: entry.Fault.Code, Reason: entry.Fault.Reason}
		}
		results = append(results, result)
	}
	return results
}

func fromNodeResults(results []run.NodeResult) []nodeResult {
	entries := make([]nodeResult, 0, len(results))
	for _, result := range results {
		entry := nodeResult{
			Participant: string(result.Node.Participant),
			NodeID:      string(result.Node.NodeID),
			State:       string(result.State),
			Image:       result.Image.String(),
			ExitCode:    result.ExitCode,
		}
		if result.Fault != nil {
			entry.Fault = &fault{Code: result.Fault.Code, Reason: result.Fault.Reason}
		}
		entries = append(entries, entry)
	}
	return entries
}

func toIdentity(node vo.NodeRef, state meshState) mesh.Identity {
	return mesh.Identity{
		Member:    mesh.Member{Node: node, Address: state.Address, PublicKey: state.PublicKey},
		Signature: state.Signature,
	}
}

func toConfig(shardID vo.ShardID, peers []peer) mesh.Config {
	config := mesh.Config{Shard: shardID, Peers: make([]mesh.Peer, 0, len(peers))}
	for _, entry := range peers {
		config.Peers = append(config.Peers, mesh.Peer{
			Rank:      entry.Rank,
			Node:      vo.NodeRef{Participant: vo.Participant(entry.Participant), NodeID: vo.NodeID(entry.NodeID)},
			Address:   entry.Address,
			PublicKey: entry.PublicKey,
		})
	}
	return config
}

func fromConfig(config mesh.Config) []peer {
	peers := make([]peer, 0, len(config.Peers))
	for _, entry := range config.Peers {
		peers = append(peers, peer{
			Rank:        entry.Rank,
			Participant: string(entry.Node.Participant),
			NodeID:      string(entry.Node.NodeID),
			Address:     entry.Address,
			PublicKey:   entry.PublicKey,
		})
	}
	return peers
}
