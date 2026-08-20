package netns

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Nodes []vo.NodeRef

	Endpoint string

	PortBase int

	KeyDir string

	IP      string
	WG      string
	Nsenter string
	NFT     string

	DeniedCIDRs []string

	Handshake time.Duration

	Settle  time.Duration
	Timeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.PortBase == 0 {
		c.PortBase = 51820
	}
	if c.KeyDir == "" {
		c.KeyDir = "/var/lib/trainshardd/mesh"
	}
	if c.IP == "" {
		c.IP = "ip"
	}
	if c.WG == "" {
		c.WG = "wg"
	}
	if c.Nsenter == "" {
		c.Nsenter = "nsenter"
	}
	if c.NFT == "" {
		c.NFT = "nft"
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
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
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
		PublicKey: base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()),
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

	inside, err := n.present(ctx, pid, device)
	if err != nil {
		return err
	}
	if !inside {
		if err := n.create(ctx, shardID, node, device, n.cfg.PortBase+slot, pid); err != nil {
			return err
		}
	}

	for _, peer := range others {
		rank, err := address(shardID, peer.Rank)
		if err != nil {
			return err
		}
		if err := n.run(ctx, n.inside(pid, n.cfg.WG, "set", device,
			"peer", peer.PublicKey,
			"endpoint", peer.Address,
			"allowed-ips", rank+"/32",
			"persistent-keepalive", "25")...); err != nil {
			return err
		}
	}

	own, err := address(shardID, self.Rank)
	if err != nil {
		return err
	}
	if err := n.run(ctx, n.inside(pid, n.cfg.IP, "address", "replace", own+"/16", "dev", device)...); err != nil {
		return err
	}
	if err := n.run(ctx, n.inside(pid, n.cfg.IP, "link", "set", device, "up")...); err != nil {
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

	up, err := n.present(ctx, pid, iface(slot))
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

	seen, err := n.handshake(ctx, pid, device, peer.PublicKey)
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
	return n.handshake(ctx, pid, device, peer.PublicKey)
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
		device := iface(slot)
		if inside, err := n.present(ctx, pid, device); err != nil {
			return err
		} else if inside {
			if err := n.run(ctx, n.inside(pid, n.cfg.IP, "link", "delete", device)...); err != nil {
				return err
			}
		}
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

func (n *Network) create(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, device string, port, pid int) error {
	if err := n.run(ctx, n.cfg.IP, "link", "add", device, "type", "wireguard"); err != nil {
		return err
	}
	if err := n.run(ctx, n.cfg.WG, "set", device, "private-key", n.keyPath(shardID, node), "listen-port", strconv.Itoa(port)); err != nil {
		return err
	}
	return n.run(ctx, n.cfg.IP, "link", "set", device, "netns", strconv.Itoa(pid))
}

func (n *Network) present(ctx context.Context, pid int, device string) (bool, error) {
	out, err := n.output(ctx, n.inside(pid, n.cfg.IP, "-oneline", "link", "show")...)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, ": "+device+":"), nil
}

func (n *Network) handshake(ctx context.Context, pid int, device, peer string) (bool, error) {
	out, err := n.output(ctx, n.inside(pid, n.cfg.WG, "show", device, "latest-handshakes")...)
	if err != nil {
		return false, err
	}

	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != peer {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || seconds == 0 {
			return false, nil
		}
		return n.clock.Now().Sub(time.Unix(seconds, 0)) < n.cfg.Handshake, nil
	}
	return false, nil
}

func (n *Network) inside(pid int, command ...string) []string {
	return append([]string{n.cfg.Nsenter, "--net=/proc/" + strconv.Itoa(pid) + "/ns/net", "--"}, command...)
}

func (n *Network) run(ctx context.Context, command ...string) error {
	_, err := n.output(ctx, command...)
	return err
}

func (n *Network) output(ctx context.Context, command ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(command, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (n *Network) script(ctx context.Context, command []string, stdin string) error {
	ctx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(command, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (n *Network) key(shardID vo.ShardID, node vo.NodeRef) (*ecdh.PrivateKey, error) {
	path := n.keyPath(shardID, node)

	raw, err := os.ReadFile(path)
	if err == nil {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("mesh key %s: %w", path, err)
		}
		return ecdh.X25519().NewPrivateKey(decoded)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(private.Bytes())
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, err
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
