package netns

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Nodes []vo.NodeRef

	Endpoint string

	PortBase int

	KeyDir string

	DeniedCIDRs []string

	Handshake time.Duration

	Settle time.Duration
}

func (c Config) withDefaults() Config {
	if c.PortBase == 0 {
		c.PortBase = 51820
	}
	if c.KeyDir == "" {
		c.KeyDir = "/var/lib/trainshardd/mesh"
	}
	if len(c.DeniedCIDRs) == 0 {
		c.DeniedCIDRs = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "fc00::/7", "fe80::/10"}
	}
	if c.Handshake == 0 {
		c.Handshake = 3 * time.Minute
	}
	if c.Settle == 0 {
		c.Settle = 5 * time.Second
	}
	return c
}

type Sandboxes interface {
	Sandbox(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (pid int, err error)
	SandboxPID(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (pid int, present bool, err error)
	RemoveSandbox(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
}

type Network struct {
	cfg     Config
	sandbox Sandboxes
	clock   ports.Clock
	log     *slog.Logger
}

func New(cfg Config, sandbox Sandboxes, clock ports.Clock, log *slog.Logger) *Network {
	return &Network{cfg: cfg.withDefaults(), sandbox: sandbox, clock: clock, log: log}
}

func (n *Network) Identity(_ context.Context, shardID vo.ShardID, node vo.NodeRef) (mesh.Member, error) {
	slot, err := n.slot(node)
	if err != nil {
		return mesh.Member{}, err
	}
	if n.cfg.Endpoint == "" {
		return mesh.Member{}, fmt.Errorf("mesh endpoint is not configured: peers would have nowhere to answer")
	}

	private, err := n.key(shardID, node)
	if err != nil {
		return mesh.Member{}, err
	}
	return mesh.Member{
		Node:      node,
		Address:   net.JoinHostPort(n.cfg.Endpoint, strconv.Itoa(n.cfg.PortBase+slot)),
		PublicKey: private.PublicKey().String(),
	}, nil
}

func (n *Network) Apply(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, peers []mesh.Peer) error {
	self, others, err := split(node, peers)
	if err != nil {
		return err
	}
	slot, err := n.slot(node)
	if err != nil {
		return err
	}

	pid, err := n.sandbox.Sandbox(ctx, shardID, node)
	if err != nil {
		return err
	}
	device := iface(slot)

	inside, err := present(pid, device)
	if err != nil {
		return err
	}
	if !inside {
		if err := n.create(shardID, node, device, n.cfg.PortBase+slot, pid); err != nil {
			return err
		}
	}

	config, err := peerConfig(shardID, others)
	if err != nil {
		return err
	}
	if err := inNetns(pid, func(wg *wgctrl.Client) error {
		return wg.ConfigureDevice(device, config)
	}); err != nil {
		return err
	}

	own, err := address(shardID, self.Rank)
	if err != nil {
		return err
	}
	if err := raise(pid, device, own); err != nil {
		return err
	}

	n.log.Info("mesh up", "node_id", node.NodeID, "device", device, "address", own, "peers", len(others))
	return nil
}

func (n *Network) Present(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, bool, error) {
	slot, err := n.slot(node)
	if err != nil {
		return false, false, err
	}

	key := true
	if _, err := os.Stat(n.keyPath(shardID, node)); errors.Is(err, fs.ErrNotExist) {
		key = false
	} else if err != nil {
		return false, false, err
	}

	pid, running, err := n.sandbox.SandboxPID(ctx, shardID, node)
	if err != nil || !running {
		return key, false, err
	}

	up, err := present(pid, iface(slot))
	return key, up, err
}

func (n *Network) Reach(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, peer mesh.Peer) (bool, error) {
	slot, err := n.slot(node)
	if err != nil {
		return false, err
	}
	pid, running, err := n.sandbox.SandboxPID(ctx, shardID, node)
	if err != nil {
		return false, err
	}
	if !running {
		return false, nil
	}
	device := iface(slot)

	seen, err := n.handshake(pid, device, peer.PublicKey)
	if err != nil || seen {
		return seen, err
	}

	settle := time.NewTimer(n.cfg.Settle)
	defer settle.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-settle.C:
	}
	return n.handshake(pid, device, peer.PublicKey)
}

func (n *Network) Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	slot, err := n.slot(node)
	if err != nil {
		return err
	}
	pid, running, err := n.sandbox.SandboxPID(ctx, shardID, node)
	if err != nil {
		return err
	}
	if running {
		if err := remove(pid, iface(slot)); err != nil {
			return err
		}
	}
	// A sandbox that is gone took its link with it, but one stranded on the host outlives it
	if err := discard(iface(slot)); err != nil {
		return err
	}

	if err := n.sandbox.RemoveSandbox(ctx, shardID, node); err != nil {
		return err
	}
	if err := os.Remove(n.keyPath(shardID, node)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	n.log.Info("mesh removed", "node_id", node.NodeID)
	return nil
}

// Between docker starting the sandbox on a bridge and the ruleset landing the namespace is open.
// Nothing can use that window: the sandbox holds a pause process and the run container is not
// created until Allow has returned, so never move Allow after container create
func (n *Network) Allow(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, sources []vo.Source) ([]run.PinnedHost, error) {
	slot, err := n.slot(node)
	if err != nil {
		return nil, err
	}
	pinned, allowed, err := n.resolve(ctx, sources)
	if err != nil {
		return nil, err
	}

	pid, err := n.sandbox.Sandbox(ctx, shardID, node)
	if err != nil {
		return nil, err
	}
	if err := fence(pid, iface(slot), n.cfg.DeniedCIDRs, allowed); err != nil {
		return nil, err
	}

	n.log.Info("egress fixed", "node_id", node.NodeID, "sources", len(sources), "allowed", len(allowed))
	return pinned, nil
}

type allowance struct {
	address net.IP
	port    int
}

func (n *Network) resolve(ctx context.Context, sources []vo.Source) ([]run.PinnedHost, []allowance, error) {
	pinned := make([]run.PinnedHost, 0, len(sources))
	allowed := make([]allowance, 0, len(sources))

	for _, source := range sources {
		if literal := net.ParseIP(source.Host); literal != nil {
			allowed = append(allowed, allowance{address: literal, port: source.Port})
			continue
		}

		found, err := net.DefaultResolver.LookupIP(ctx, "ip", source.Host)
		if err != nil {
			return nil, nil, fmt.Errorf("source %q does not resolve: %w", source.Host, err)
		}
		for _, address := range found {
			allowed = append(allowed, allowance{address: address, port: source.Port})
			pinned = append(pinned, run.PinnedHost{Name: source.Host, IP: address.String()})
		}
	}
	return pinned, allowed, nil
}

func (n *Network) MeshPortReachable(ctx context.Context) error {
	if err := n.dialable(ctx); err != nil {
		return err
	}
	return n.bindable(ctx)
}

func (n *Network) dialable(ctx context.Context) error {
	if n.cfg.Endpoint == "" {
		return fmt.Errorf("mesh endpoint is not configured")
	}

	address := net.ParseIP(n.cfg.Endpoint)
	if address == nil {
		if _, err := net.DefaultResolver.LookupHost(ctx, n.cfg.Endpoint); err != nil {
			return fmt.Errorf("mesh endpoint %q does not resolve: %w", n.cfg.Endpoint, err)
		}
		return nil
	}
	if address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() {
		return fmt.Errorf("mesh endpoint %s is not reachable from outside the host", address)
	}
	return nil
}

// bindable opens every mesh port this host owns, so a squatter is found before a node is leased
// rather than when the mesh is being built; a running node's port is not held here, its wireguard
// link lives in the sandbox namespace
func (n *Network) bindable(ctx context.Context) error {
	var open net.ListenConfig

	for slot := range n.cfg.Nodes {
		port := n.cfg.PortBase + slot
		socket, err := open.ListenPacket(ctx, "udp", net.JoinHostPort("", strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("mesh port %d is taken: %w", port, err)
		}
		if err := socket.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (n *Network) create(shardID vo.ShardID, node vo.NodeRef, device string, port, pid int) error {
	private, err := n.key(shardID, node)
	if err != nil {
		return err
	}
	return build(device, wgtypes.Config{PrivateKey: &private, ListenPort: &port}, pid)
}

func (n *Network) handshake(pid int, device, peer string) (bool, error) {
	key, err := wgtypes.ParseKey(peer)
	if err != nil {
		return false, fmt.Errorf("peer key %q: %w", peer, err)
	}

	var last time.Time
	if err := inNetns(pid, func(wg *wgctrl.Client) error {
		found, err := wg.Device(device)
		if err != nil {
			return err
		}
		for _, known := range found.Peers {
			if known.PublicKey == key {
				last = known.LastHandshakeTime
			}
		}
		return nil
	}); err != nil {
		return false, err
	}

	if last.IsZero() {
		return false, nil
	}
	return n.clock.Now().Sub(last) < n.cfg.Handshake, nil
}

func (n *Network) key(shardID vo.ShardID, node vo.NodeRef) (wgtypes.Key, error) {
	path := n.keyPath(shardID, node)

	raw, err := os.ReadFile(path)
	if err == nil {
		key, err := wgtypes.ParseKey(strings.TrimSpace(string(raw)))
		if err != nil {
			return wgtypes.Key{}, fmt.Errorf("mesh key %s: %w", path, err)
		}
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return wgtypes.Key{}, err
	}

	private, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return wgtypes.Key{}, err
	}
	if err := os.WriteFile(path, []byte(private.String()+"\n"), 0o600); err != nil {
		return wgtypes.Key{}, err
	}

	n.log.Info("created mesh key", "node_id", node.NodeID)
	return private, nil
}

func (n *Network) Shards(_ context.Context, node vo.NodeRef) ([]vo.ShardID, error) {
	matches, err := filepath.Glob(filepath.Join(n.cfg.KeyDir, "*_"+string(node.NodeID)+".key"))
	if err != nil {
		return nil, err
	}

	held := make([]vo.ShardID, 0, len(matches))
	for _, path := range matches {
		name, _, _ := strings.Cut(filepath.Base(path), "_")
		shardID, err := vo.ParseShardID(name)
		if err != nil {
			continue
		}
		held = append(held, shardID)
	}
	return held, nil
}

func (n *Network) keyPath(shardID vo.ShardID, node vo.NodeRef) string {
	return filepath.Join(n.cfg.KeyDir, fmt.Sprintf("%s_%s.key", shardID, node.NodeID))
}

func (n *Network) slot(node vo.NodeRef) (int, error) {
	for index, held := range n.cfg.Nodes {
		if held == node {
			return index, nil
		}
	}
	return 0, fmt.Errorf("node %s is not one of this host's nodes", node)
}

// peerConfig replaces the peer list instead of adding to it, so a node dropped from the mesh
// stops being a peer of the nodes that stayed
func peerConfig(shardID vo.ShardID, peers []mesh.Peer) (wgtypes.Config, error) {
	keepalive := 25 * time.Second
	configured := make([]wgtypes.PeerConfig, 0, len(peers))

	for _, peer := range peers {
		key, err := wgtypes.ParseKey(peer.PublicKey)
		if err != nil {
			return wgtypes.Config{}, fmt.Errorf("peer key %q: %w", peer.PublicKey, err)
		}
		endpoint, err := net.ResolveUDPAddr("udp", peer.Address)
		if err != nil {
			return wgtypes.Config{}, fmt.Errorf("peer address %q: %w", peer.Address, err)
		}
		own, err := address(shardID, peer.Rank)
		if err != nil {
			return wgtypes.Config{}, err
		}
		_, allowed, err := net.ParseCIDR(own + "/32")
		if err != nil {
			return wgtypes.Config{}, err
		}

		configured = append(configured, wgtypes.PeerConfig{
			PublicKey:                   key,
			Endpoint:                    endpoint,
			AllowedIPs:                  []net.IPNet{*allowed},
			PersistentKeepaliveInterval: &keepalive,
			ReplaceAllowedIPs:           true,
		})
	}
	return wgtypes.Config{ReplacePeers: true, Peers: configured}, nil
}

func iface(slot int) string {
	return fmt.Sprintf("ts%d", slot)
}

func address(shardID vo.ShardID, rank int) (string, error) {
	if rank < 0 || rank > 253 {
		return "", fmt.Errorf("rank %d has no address on the mesh", rank)
	}
	return fmt.Sprintf("10.%d.0.%d", uint64(shardID)%256, rank+1), nil
}

func split(node vo.NodeRef, peers []mesh.Peer) (mesh.Peer, []mesh.Peer, error) {
	var self mesh.Peer
	found := false
	others := make([]mesh.Peer, 0, len(peers))

	for _, peer := range peers {
		if peer.Node == node {
			self, found = peer, true
			continue
		}
		others = append(others, peer)
	}
	if !found {
		return mesh.Peer{}, nil, fmt.Errorf("node %s is not in its own peer list", node)
	}
	return self, others, nil
}
