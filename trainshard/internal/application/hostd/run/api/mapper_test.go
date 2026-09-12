package api_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"trainshard/internal/application/hostd/run/api"
	"trainshard/internal/contract"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

const (
	participant = vo.Participant("gonka1host")
	otherHost   = "gonka1peer"
)

var (
	actor    = shard.Actor{Address: "gonka1creator"}
	deadline = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	digest   = "ghcr.io/gonka/train@sha256:" + strings.Repeat("a", 64)
)

func command() contract.Command {
	return contract.Command{
		ShardID:   "7",
		NodeIDs:   []string{"node-a"},
		RequestID: "req-1",
		Deadline:  deadline.Format(time.RFC3339),
	}
}

func TestToNodesCommand(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		mutate func(*contract.Command)
		valid  bool
	}{
		{name: "shard in the path and the body agree", path: "7", mutate: func(*contract.Command) {}, valid: true},
		{name: "body may leave the shard out", path: "7", mutate: func(c *contract.Command) { c.ShardID = "" }, valid: true},
		{name: "body names another shard", path: "7", mutate: func(c *contract.Command) { c.ShardID = "8" }},
		{name: "shard id is not a number", path: "seven", mutate: func(*contract.Command) {}},
		{name: "shard id zero", path: "0", mutate: func(*contract.Command) {}},
		{name: "no node ids", path: "7", mutate: func(c *contract.Command) { c.NodeIDs = nil }},
		{name: "empty node id", path: "7", mutate: func(c *contract.Command) { c.NodeIDs = []string{""} }},
		{name: "no request id", path: "7", mutate: func(c *contract.Command) { c.RequestID = "" }},
		{name: "deadline is not a timestamp", path: "7", mutate: func(c *contract.Command) { c.Deadline = "tomorrow" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			dto := command()
			tc.mutate(&dto)

			cmd, err := api.ToNodesCommand(participant, actor, tc.path, dto)

			if tc.valid {
				if err != nil {
					t.Fatalf("got %v, want no error", err)
				}
				if cmd.Shard != 7 || !cmd.Deadline.Equal(deadline) || cmd.Actor != actor {
					t.Fatalf("got %+v, want the parsed command", cmd)
				}
				return
			}
			if !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}

func TestToNodesCommandNamesNodesUnderTheHostItRunsOn(t *testing.T) {

	dto := command()
	dto.NodeIDs = []string{"node-a", "node-a", "node-b"}

	cmd, err := api.ToNodesCommand(participant, actor, "7", dto)

	if err != nil {
		t.Fatalf("map: %v", err)
	}
	want := []vo.NodeRef{{Participant: participant, NodeID: "node-a"}, {Participant: participant, NodeID: "node-b"}}
	if len(cmd.Nodes) != len(want) || cmd.Nodes[0] != want[0] || cmd.Nodes[1] != want[1] {
		t.Fatalf("got %v, want %v with the repeat collapsed", cmd.Nodes, want)
	}
}

func TestToDeployCommandNeedsADigest(t *testing.T) {
	cases := []struct {
		name  string
		image string
		valid bool
	}{
		{name: "digest", image: digest, valid: true},
		{name: "tag", image: "ghcr.io/gonka/train:v1"},
		{name: "nothing", image: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			dto := contract.DeployRequest{Command: command(), ImageDigest: tc.image, GPUs: 8, DiskBytes: 1 << 30}

			cmd, err := api.ToDeployCommand(participant, actor, "7", dto)

			if tc.valid {
				if err != nil || cmd.Run.Image.String() != tc.image {
					t.Fatalf("got %+v (%v), want the digest kept", cmd.Run, err)
				}
				return
			}
			if !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}

func TestToDeployCommandParsesTheSourcesTheRunDeclares(t *testing.T) {
	cases := []struct {
		name    string
		sources []string
		want    int
		valid   bool
	}{
		{name: "none declared", want: 0, valid: true},
		{name: "host and port", sources: []string{"s3.amazonaws.com:443"}, want: 1, valid: true},
		{name: "the same one twice", sources: []string{"s3.amazonaws.com:443", "s3.amazonaws.com:443"}, want: 1, valid: true},
		{name: "no port", sources: []string{"s3.amazonaws.com"}},
		{name: "port out of range", sources: []string{"s3.amazonaws.com:70000"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			dto := contract.DeployRequest{Command: command(), ImageDigest: digest, Sources: tc.sources, GPUs: 8, DiskBytes: 1 << 30}

			cmd, err := api.ToDeployCommand(participant, actor, "7", dto)

			if tc.valid {
				if err != nil || len(cmd.Run.Sources) != tc.want {
					t.Fatalf("got %v (%v), want %d sources", cmd.Run.Sources, err, tc.want)
				}
				return
			}
			if !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}

func meshRequest() contract.MeshRequest {
	return contract.MeshRequest{
		Command: command(),
		Peers: []contract.Peer{
			{Rank: 0, Participant: string(participant), NodeID: "node-a", Address: "10.0.0.1", PublicKey: "key-a"},
			{Rank: 1, Participant: otherHost, NodeID: "node-b", Address: "10.0.0.2", PublicKey: "key-b"},
		},
	}
}

func TestToMeshCommandRebuildsTheOrderingItWasHanded(t *testing.T) {

	dto := meshRequest()

	cmd, err := api.ToMeshCommand(participant, actor, "7", dto)

	if err != nil {
		t.Fatalf("map: %v", err)
	}
	master, ok := cmd.Config.Master()
	if !ok || master.Node.NodeID != "node-a" {
		t.Fatalf("got master %+v, want the lowest node at rank 0", master)
	}
}

func TestToMeshCommandRefusesRanksItCannotDeriveItself(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*contract.MeshRequest)
	}{
		{
			name:   "ranks swapped by the coordinator",
			mutate: func(m *contract.MeshRequest) { m.Peers[0].Rank, m.Peers[1].Rank = 1, 0 },
		},
		{
			name: "same node twice",
			mutate: func(m *contract.MeshRequest) {
				m.Peers[1].Participant = string(participant)
				m.Peers[1].NodeID = "node-a"
			},
		},
		{
			name:   "peer without a public key",
			mutate: func(m *contract.MeshRequest) { m.Peers[1].PublicKey = "" },
		},
		{
			name:   "no peers at all",
			mutate: func(m *contract.MeshRequest) { m.Peers = nil },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			dto := meshRequest()
			tc.mutate(&dto)

			_, err := api.ToMeshCommand(participant, actor, "7", dto)

			if !errors.Is(err, shared.ErrValidation) {
				t.Fatalf("got %v, want a validation error", err)
			}
		})
	}
}

func TestToNodesOutputAlwaysCarriesAList(t *testing.T) {

	empty := api.ToNodesOutput(nil)
	failed := api.ToNodesOutput([]run.NodeResult{run.Failed(vo.NodeRef{NodeID: "node-a"}, mesh.ErrNodeNotInMesh)})

	if empty.Items == nil {
		t.Fatal("items must serialize as an empty list, never null")
	}
	if failed.Items[0].Error == nil || failed.Items[0].Error.Code != "MESH_NODE_NOT_IN_MESH" {
		t.Fatalf("got %+v, want the node's own failure code", failed.Items[0].Error)
	}
}
