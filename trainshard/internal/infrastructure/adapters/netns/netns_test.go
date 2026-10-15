package netns

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
)

const shard = vo.ShardID(42)

func ref(id string) vo.NodeRef {
	return vo.NodeRef{Participant: "gonka1abc", NodeID: vo.NodeID(id)}
}

func network(t *testing.T, cfg Config) *Network {
	t.Helper()

	cfg.KeyDir = t.TempDir()
	return New(cfg, nil, nil, slog.New(slog.DiscardHandler))
}

type runningSandbox struct{ Sandboxes }

func (runningSandbox) SandboxPID(context.Context, vo.ShardID, vo.NodeRef) (int, bool, error) {
	return 1, true, nil
}

func TestConfined(t *testing.T) {
	// arrange
	host, daemon := nsID{dev: 4, ino: 4026531840}, nsID{dev: 4, ino: 4026532001}
	cases := []struct {
		name    string
		sandbox nsID
		refused bool
	}{
		{name: "a namespace of its own", sandbox: nsID{dev: 4, ino: 4026532500}},
		{name: "the host's namespace under a reused pid", sandbox: host, refused: true},
		{name: "the daemon's own namespace", sandbox: daemon, refused: true},
		{name: "the host's inode on another device", sandbox: nsID{dev: 5, ino: host.ino}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			err := confined(1234, tc.sandbox, host, daemon)

			// assert
			if tc.refused && !errors.Is(err, errNotSandbox) {
				t.Fatalf("err = %v, want the sandbox refused before anything is written", err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("a sandbox in its own namespace was refused: %v", err)
			}
		})
	}
}

func TestSlotsDoNotCollide(t *testing.T) {
	// assert
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
	cfg, err := peerConfig(shard, peers, false)

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

func TestWantedPeersNeedNoLookup(t *testing.T) {
	// arrange
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	peers := []mesh.Peer{{
		Rank:      1,
		Node:      ref("b"),
		Address:   "peer.invalid:51821",
		PublicKey: key.PublicKey().String(),
	}}

	// act
	cfg, err := wanted(shard, peers)

	// assert
	if err != nil {
		t.Fatalf("a name that does not resolve must not fail the comparison: %v", err)
	}
	if len(cfg.Peers) != 1 || cfg.Peers[0].PublicKey != key.PublicKey() || cfg.Peers[0].Endpoint != nil {
		t.Fatalf("peers = %+v, want the key without an endpoint", cfg.Peers)
	}
	if len(cfg.Peers[0].AllowedIPs) != 1 || cfg.Peers[0].AllowedIPs[0].String() != "10.42.0.2/32" {
		t.Fatalf("allowed ips = %v", cfg.Peers[0].AllowedIPs)
	}
}

func TestPeerConfigRefusesARankOffTheMesh(t *testing.T) {
	// arrange
	peer := mesh.Peer{Rank: 999, Address: "203.0.113.9:51821", PublicKey: mustKey(t).String()}

	// act
	_, err := peerConfig(shard, []mesh.Peer{peer}, false)

	// assert
	if err == nil {
		t.Fatal("want an error rather than a half-configured mesh")
	}
}

func TestPeerConfigLeavesOutAPeerWithAnUnusableKey(t *testing.T) {
	// arrange
	good := mustKey(t)
	peers := []mesh.Peer{
		{Rank: 1, Node: ref("b"), Address: "203.0.113.9:51821", PublicKey: "nope"},
		{Rank: 2, Node: ref("c"), Address: "203.0.113.10:51820", PublicKey: good.String()},
	}

	// act
	cfg, err := peerConfig(shard, peers, false)
	want, wantErr := wanted(shard, peers)

	// assert
	if err != nil || wantErr != nil {
		t.Fatalf("one member's bad key must not cost this node its mesh: %v, %v", err, wantErr)
	}
	if len(cfg.Peers) != 1 || cfg.Peers[0].PublicKey != good {
		t.Fatalf("peers = %+v, want only the peer with a usable key", cfg.Peers)
	}
	if cfg.Peers[0].Endpoint == nil || cfg.Peers[0].Endpoint.String() != "203.0.113.10:51820" {
		t.Fatalf("endpoint = %v, want the good peer's own", cfg.Peers[0].Endpoint)
	}
	if !samePeers(asPeers(cfg.Peers), want.Peers) {
		t.Fatal("what is applied and what is checked as up must leave out the same peers")
	}
}

func TestPeerConfigDialsOnlyWhereAPeerCanStand(t *testing.T) {
	// arrange
	cases := []struct {
		name    string
		address string
		private bool
		dialed  string
	}{
		{name: "public address", address: "203.0.113.9:51821", dialed: "203.0.113.9:51821"},
		{name: "public ipv6 address", address: "[2001:db8::9]:51821", dialed: "[2001:db8::9]:51821"},
		{name: "address without a port", address: "203.0.113.9"},
		{name: "port off the range", address: "203.0.113.9:70000"},
		{name: "no host", address: ":51821"},
		{name: "loopback", address: "127.0.0.1:22"},
		{name: "loopback on a private mesh", address: "127.0.0.1:22", private: true},
		{name: "unspecified", address: "0.0.0.0:51821"},
		{name: "link-local", address: "169.254.169.254:51821"},
		{name: "multicast", address: "224.0.0.1:51821"},
		{name: "private address on a public mesh", address: "10.1.2.3:51821"},
		{name: "private address on a private mesh", address: "10.1.2.3:51821", private: true, dialed: "10.1.2.3:51821"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			peers := []mesh.Peer{{Rank: 1, Node: ref("b"), Address: tc.address, PublicKey: mustKey(t).String()}}

			// act
			cfg, err := peerConfig(shard, peers, tc.private)

			// assert
			if err != nil {
				t.Fatalf("one member's address must not cost this node its mesh: %v", err)
			}
			if len(cfg.Peers) != 1 {
				t.Fatalf("peers = %d, want the peer kept so it can still call in", len(cfg.Peers))
			}
			got := ""
			if cfg.Peers[0].Endpoint != nil {
				got = cfg.Peers[0].Endpoint.String()
			}
			if got != tc.dialed {
				t.Fatalf("endpoint = %q, want %q", got, tc.dialed)
			}
		})
	}
}

func TestReachTakesAnUnusablePeerKeyAsUnreached(t *testing.T) {
	// arrange
	n := network(t, Config{Nodes: []vo.NodeRef{ref("a")}})
	n.sandbox = runningSandbox{}
	peer := mesh.Peer{Rank: 1, Node: ref("b"), Address: "203.0.113.9:51821", PublicKey: "nope"}

	// act
	reached, err := n.Reach(context.Background(), shard, ref("a"), peer)

	// assert
	if err != nil {
		t.Fatalf("a bad peer key must read as not reached, or every node probing it is cut off: %v", err)
	}
	if reached {
		t.Fatal("a peer with no usable key cannot have been reached")
	}
}

func TestAHandshakeIsRecentOnlyWithinTheWindow(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"never", time.Time{}, false},
		{"a minute ago", now.Add(-time.Minute), true},
		{"at the window's edge", now.Add(-3 * time.Minute), false},
		{"long ago", now.Add(-time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			n := New(Config{}, nil, timex.NewFrozen(now), slog.New(slog.DiscardHandler))

			// act
			got := n.recent(tc.at)

			// assert
			if got != tc.want {
				t.Fatalf("recent(%v) = %t, want %t", tc.at, got, tc.want)
			}
		})
	}
}

type goneSandbox struct{ Sandboxes }

func (goneSandbox) SandboxPID(context.Context, vo.ShardID, vo.NodeRef) (int, bool, error) {
	return 0, false, nil
}

func TestSilentNamesNoPeerWithoutAnInterface(t *testing.T) {
	// arrange
	n := network(t, Config{Nodes: []vo.NodeRef{ref("a")}})
	n.sandbox = goneSandbox{}
	peers := []mesh.Peer{{Rank: 0, Node: ref("a")}, {Rank: 1, Node: ref("b")}}

	// act
	silent, err := n.Silent(context.Background(), shard, ref("a"), peers)

	// assert
	if err != nil || len(silent) != 0 {
		t.Fatalf("got %v, %v, want no peers named for a node with no interface", silent, err)
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
	for _, name := range []string{"7_a.key", "9_a.key", "11_b.key", "13_b_a.key", "notashard_a.key"} {
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
	names := map[string][]net.IP{
		"mesh.example.com":    {net.ParseIP("198.51.100.7")},
		"10-1-2-3.sslip.io":   {net.ParseIP("10.1.2.3")},
		"split.example.com":   {net.ParseIP("198.51.100.7"), net.ParseIP("192.168.1.5")},
		"loopback.example.io": {net.ParseIP("127.0.0.1")},
	}
	cases := []struct {
		name     string
		endpoint string
		private  bool
		refused  bool
	}{
		{name: "public address", endpoint: "198.51.100.7"},
		{name: "not configured", endpoint: "", refused: true},
		{name: "private address", endpoint: "10.1.2.3", refused: true},
		{name: "loopback", endpoint: "127.0.0.1", refused: true},
		{name: "unspecified", endpoint: "0.0.0.0", refused: true},
		{name: "link-local", endpoint: "169.254.1.1", private: true, refused: true},
		{name: "name of a public address", endpoint: "mesh.example.com"},
		{name: "name of a private address", endpoint: "10-1-2-3.sslip.io", refused: true},
		{name: "name with a private address among others", endpoint: "split.example.com", refused: true},
		{name: "name that does not resolve", endpoint: "nowhere.example.com", refused: true},
		{name: "private address asked for", endpoint: "10.1.2.3", private: true},
		{name: "name of a private address asked for", endpoint: "10-1-2-3.sslip.io", private: true},
		{name: "loopback asked for as private", endpoint: "127.0.0.1", private: true, refused: true},
		{name: "name of loopback asked for as private", endpoint: "loopback.example.io", private: true, refused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			n := network(t, Config{Endpoint: tc.endpoint, Private: tc.private})
			n.lookup = func(_ context.Context, _, host string) ([]net.IP, error) {
				if found, ok := names[host]; ok {
					return found, nil
				}
				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}

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

func TestSamePeers(t *testing.T) {
	// arrange
	keyA, keyB := mustKey(t), mustKey(t)
	have := []wgtypes.Peer{
		{PublicKey: keyA, AllowedIPs: []net.IPNet{cidr("10.42.0.2/32")}},
		{PublicKey: keyB, AllowedIPs: []net.IPNet{cidr("10.42.0.3/32")}},
	}
	cases := []struct {
		name string
		want []wgtypes.PeerConfig
		same bool
	}{
		{
			name: "same keys and addresses in another order",
			want: []wgtypes.PeerConfig{
				{PublicKey: keyB, AllowedIPs: []net.IPNet{cidr("10.42.0.3/32")}},
				{PublicKey: keyA, AllowedIPs: []net.IPNet{cidr("10.42.0.2/32")}},
			},
			same: true,
		},
		{
			name: "a peer was dropped",
			want: []wgtypes.PeerConfig{{PublicKey: keyA, AllowedIPs: []net.IPNet{cidr("10.42.0.2/32")}}},
		},
		{
			name: "a peer moved to another rank",
			want: []wgtypes.PeerConfig{
				{PublicKey: keyA, AllowedIPs: []net.IPNet{cidr("10.42.0.2/32")}},
				{PublicKey: keyB, AllowedIPs: []net.IPNet{cidr("10.42.0.4/32")}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got := samePeers(have, tc.want)

			// assert
			if got != tc.same {
				t.Fatalf("got %v, want %v", got, tc.same)
			}
		})
	}
}

func mustKey(t *testing.T) wgtypes.Key {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key.PublicKey()
}

func asPeers(configured []wgtypes.PeerConfig) []wgtypes.Peer {
	peers := make([]wgtypes.Peer, 0, len(configured))
	for _, peer := range configured {
		peers = append(peers, wgtypes.Peer{PublicKey: peer.PublicKey, AllowedIPs: peer.AllowedIPs})
	}
	return peers
}

func cidr(s string) net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return *network
}
