package netns

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

const shard = vo.ShardID(42)

func ref(id string) vo.NodeRef {
	return vo.NodeRef{Participant: "gonka1abc", NodeID: vo.NodeID(id)}
}

// network builds the adapter with a key directory of its own. The sandbox and the clock stay nil:
// every function reached from here is one that never enters a namespace
func network(t *testing.T, cfg Config) *Network {
	t.Helper()

	cfg.KeyDir = t.TempDir()
	return New(cfg, nil, nil, slog.New(slog.DiscardHandler))
}

func TestAddress(t *testing.T) {
	// arrange
	cases := []struct {
		name    string
		shard   vo.ShardID
		rank    int
		want    string
		refused bool
	}{
		{name: "first rank", shard: shard, rank: 0, want: "10.42.0.1"},
		{name: "second rank", shard: shard, rank: 1, want: "10.42.0.2"},
		{name: "last rank that fits", shard: shard, rank: 253, want: "10.42.0.254"},
		{name: "shard wraps at 256", shard: vo.ShardID(258), rank: 0, want: "10.2.0.1"},
		{name: "negative rank", shard: shard, rank: -1, refused: true},
		{name: "rank past the subnet", shard: shard, rank: 254, refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got, err := address(tc.shard, tc.rank)

			// assert
			if tc.refused {
				if err == nil {
					t.Fatalf("address(%d, %d) = %q, want an error", tc.shard, tc.rank, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("address(%d, %d) = %q, want %q", tc.shard, tc.rank, got, tc.want)
			}
		})
	}
}

// Two nodes of the same shard on one host must not land on the same address or interface
func TestRanksAndSlotsDoNotCollide(t *testing.T) {
	// act
	first, _ := address(shard, 0)
	second, _ := address(shard, 1)

	// assert
	if first == second {
		t.Fatalf("ranks 0 and 1 share %q", first)
	}
	if iface(0) == iface(1) {
		t.Fatalf("slots 0 and 1 share %q", iface(0))
	}
	if iface(3) != "ts3" {
		t.Fatalf("iface(3) = %q", iface(3))
	}
}

func TestSplit(t *testing.T) {
	// arrange
	self, other, third := ref("a"), ref("b"), ref("c")
	peers := []mesh.Peer{
		{Rank: 0, Node: other},
		{Rank: 1, Node: self},
		{Rank: 2, Node: third},
	}

	t.Run("the node is taken out of its own peer list", func(t *testing.T) {
		// act
		mine, others, err := split(self, peers)

		// assert
		if err != nil {
			t.Fatal(err)
		}
		if mine.Node != self || mine.Rank != 1 {
			t.Fatalf("self = %+v", mine)
		}
		got := []vo.NodeRef{others[0].Node, others[1].Node}
		if !slices.Equal(got, []vo.NodeRef{other, third}) {
			t.Fatalf("others = %v, want the input order kept", got)
		}
	})

	t.Run("a node missing from its own list is refused", func(t *testing.T) {
		// act
		_, _, err := split(ref("stranger"), peers)

		// assert
		if err == nil {
			t.Fatal("want an error: a node with no rank has no address")
		}
	})
}

func TestSlot(t *testing.T) {
	// arrange
	n := network(t, Config{Nodes: []vo.NodeRef{ref("a"), ref("b")}})

	// act
	second, err := n.slot(ref("b"))

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if second != 1 {
		t.Fatalf("slot = %d, want 1", second)
	}
	if _, err := n.slot(ref("stranger")); err == nil {
		t.Fatal("want an error for a node this host does not hold")
	}
}

func TestPeerConfig(t *testing.T) {
	// arrange
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	peers := []mesh.Peer{{
		Rank:      1,
		Node:      ref("b"),
		Address:   "203.0.113.9:51821",
		PublicKey: key.PublicKey().String(),
	}}

	// act
	cfg, err := peerConfig(shard, peers)

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ReplacePeers {
		t.Fatal("the peer list must be replaced, or a kicked node stays a peer of the ones that stayed")
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("peers = %d, want 1", len(cfg.Peers))
	}

	peer := cfg.Peers[0]
	if peer.PublicKey != key.PublicKey() {
		t.Fatal("public key did not survive the round trip")
	}
	if peer.Endpoint.String() != "203.0.113.9:51821" {
		t.Fatalf("endpoint = %s", peer.Endpoint)
	}
	if len(peer.AllowedIPs) != 1 || peer.AllowedIPs[0].String() != "10.42.0.2/32" {
		t.Fatalf("allowed ips = %v, want only the peer's own mesh address", peer.AllowedIPs)
	}
	if peer.PersistentKeepaliveInterval == nil || *peer.PersistentKeepaliveInterval != 25*time.Second {
		t.Fatalf("keepalive = %v", peer.PersistentKeepaliveInterval)
	}
}

func TestPeerConfigRefusesUnusablePeers(t *testing.T) {
	// arrange
	good, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		peer mesh.Peer
	}{
		{name: "key that is not a wireguard key", peer: mesh.Peer{Rank: 1, Address: "203.0.113.9:51821", PublicKey: "nope"}},
		{name: "address without a port", peer: mesh.Peer{Rank: 1, Address: "203.0.113.9", PublicKey: good.PublicKey().String()}},
		{name: "rank with no address on the mesh", peer: mesh.Peer{Rank: 999, Address: "203.0.113.9:51821", PublicKey: good.PublicKey().String()}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			_, err := peerConfig(shard, []mesh.Peer{tc.peer})

			// assert
			if err == nil {
				t.Fatal("want an error rather than a half-configured mesh")
			}
		})
	}
}

func TestKeyIsCreatedOnceAndReused(t *testing.T) {
	// arrange
	n := network(t, Config{Nodes: []vo.NodeRef{ref("a")}})

	// act
	first, err := n.key(shard, ref("a"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := n.key(shard, ref("a"))
	if err != nil {
		t.Fatal(err)
	}

	// assert
	if first != again {
		t.Fatal("a second call generated a new key, which would break every peer already holding the old one")
	}

	path := n.keyPath(shard, ref("a"))
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && stat.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v, want 0600 for private key material", stat.Mode().Perm())
	}

	// act
	other, err := n.key(shard, ref("b"))
	if err != nil {
		t.Fatal(err)
	}

	// assert
	if other == first {
		t.Fatal("two nodes share a private key")
	}
}

func TestKeyRejectsGarbageOnDisk(t *testing.T) {
	// arrange
	n := network(t, Config{Nodes: []vo.NodeRef{ref("a")}})
	if err := os.WriteFile(n.keyPath(shard, ref("a")), []byte("not-a-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// act
	_, err := n.key(shard, ref("a"))

	// assert
	if err == nil {
		t.Fatal("want an error rather than silently generating a key the peers do not know")
	}
}

func TestIdentity(t *testing.T) {
	// arrange
	n := network(t, Config{Nodes: []vo.NodeRef{ref("a"), ref("b")}, Endpoint: "198.51.100.7", PortBase: 51820})

	// act
	member, err := n.Identity(context.Background(), shard, ref("b"))

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if member.Node != ref("b") {
		t.Fatalf("node = %v", member.Node)
	}
	if member.Address != "198.51.100.7:51821" {
		t.Fatalf("address = %q, want the endpoint plus this node's slot", member.Address)
	}
	if _, err := wgtypes.ParseKey(member.PublicKey); err != nil {
		t.Fatalf("public key %q is not a wireguard key: %v", member.PublicKey, err)
	}
}

func TestIdentityRefusesAnUnusableHost(t *testing.T) {
	t.Run("no endpoint means peers have nowhere to answer", func(t *testing.T) {
		// arrange
		n := network(t, Config{Nodes: []vo.NodeRef{ref("a")}})

		// act
		_, err := n.Identity(context.Background(), shard, ref("a"))

		// assert
		if err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("a node this host does not hold", func(t *testing.T) {
		// arrange
		n := network(t, Config{Nodes: []vo.NodeRef{ref("a")}, Endpoint: "198.51.100.7"})

		// act
		_, err := n.Identity(context.Background(), shard, ref("stranger"))

		// assert
		if err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestShardsFromKeysOnDisk(t *testing.T) {
	// arrange
	n := network(t, Config{Nodes: []vo.NodeRef{ref("a")}})
	for _, name := range []string{"7_a.key", "9_a.key", "11_b.key", "notashard_a.key"} {
		if err := os.WriteFile(filepath.Join(n.cfg.KeyDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// act
	held, err := n.Shards(context.Background(), ref("a"))
	if err != nil {
		t.Fatal(err)
	}

	// assert
	slices.Sort(held)
	if !slices.Equal(held, []vo.ShardID{7, 9}) {
		t.Fatalf("shards = %v, want only this node's parsable keys", held)
	}
}

func TestDialable(t *testing.T) {
	// arrange
	cases := []struct {
		name     string
		endpoint string
		refused  bool
	}{
		{name: "public address", endpoint: "198.51.100.7"},
		{name: "not configured", endpoint: "", refused: true},
		{name: "private address", endpoint: "10.1.2.3", refused: true},
		{name: "loopback", endpoint: "127.0.0.1", refused: true},
		{name: "unspecified", endpoint: "0.0.0.0", refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			n := network(t, Config{Endpoint: tc.endpoint})

			// act
			err := n.dialable(context.Background())

			// assert
			if tc.refused && err == nil {
				t.Fatalf("endpoint %q was accepted, want a refusal", tc.endpoint)
			}
			if !tc.refused && err != nil {
				t.Fatalf("endpoint %q: %v", tc.endpoint, err)
			}
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	// act
	cfg := Config{}.withDefaults()

	// assert
	if cfg.PortBase != 51820 || cfg.KeyDir == "" {
		t.Fatalf("got %+v", cfg)
	}
	if cfg.Handshake != 3*time.Minute || cfg.Settle != 5*time.Second {
		t.Fatalf("timings = %v, %v", cfg.Handshake, cfg.Settle)
	}
	if !slices.Contains(cfg.DeniedCIDRs, "10.0.0.0/8") || !slices.Contains(cfg.DeniedCIDRs, "169.254.0.0/16") {
		t.Fatalf("denied cidrs = %v, want the private ranges and link-local", cfg.DeniedCIDRs)
	}
}
