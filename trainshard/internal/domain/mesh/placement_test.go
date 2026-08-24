package mesh_test

import (
	"errors"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

func TestAddress(t *testing.T) {
	// arrange
	cases := []struct {
		name    string
		shard   vo.ShardID
		rank    int
		want    string
		refused bool
	}{
		{name: "first rank", shard: shardID, rank: 0, want: "10.7.0.1"},
		{name: "second rank", shard: shardID, rank: 1, want: "10.7.0.2"},
		{name: "last rank that fits", shard: shardID, rank: 253, want: "10.7.0.254"},
		{name: "shard wraps at 256", shard: vo.ShardID(258), rank: 0, want: "10.2.0.1"},
		{name: "negative rank", shard: shardID, rank: -1, refused: true},
		{name: "rank past the subnet", shard: shardID, rank: 254, refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got, err := mesh.Address(tc.shard, tc.rank)

			// assert
			if tc.refused {
				if !errors.Is(err, mesh.ErrRankOffMesh) {
					t.Fatalf("got %q, %v, want %v", got, err, mesh.ErrRankOffMesh)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Two nodes of the same shard must not land on the same address
func TestAddressesDoNotCollide(t *testing.T) {
	// act
	first, _ := mesh.Address(shardID, 0)
	second, _ := mesh.Address(shardID, 1)

	// assert
	if first == second {
		t.Fatalf("ranks 0 and 1 share %q", first)
	}
}

func TestConfigPlacement(t *testing.T) {
	// arrange
	config := configOf(nodeA, nodeB, nodeC)

	t.Run("a member is placed by its rank", func(t *testing.T) {
		// act
		got, err := config.Placement(nodeB)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		want := vo.Placement{Rank: 1, Size: 3, Master: "10.7.0.1"}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("rank 0 meets itself", func(t *testing.T) {
		// act
		got, err := config.Placement(nodeA)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		own, _ := mesh.Address(shardID, got.Rank)
		if got.Rank != 0 || got.Master != own {
			t.Fatalf("got %+v, want rank 0 to be its own rendezvous", got)
		}
	})

	t.Run("a stranger has no place on the mesh", func(t *testing.T) {
		// act
		_, err := config.Placement(vo.NodeRef{Participant: hostA, NodeID: "stranger"})

		// assert
		if !errors.Is(err, mesh.ErrNodeNotInMesh) {
			t.Fatalf("got %v, want %v", err, mesh.ErrNodeNotInMesh)
		}
	})
}
