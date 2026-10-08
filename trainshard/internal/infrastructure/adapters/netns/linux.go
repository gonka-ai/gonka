//go:build linux

package netns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"slices"
	"strconv"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	ns "github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// hostPID is the machine's init only because the daemon runs with pid: host; run any other way,
// pid 1 is the daemon and the mesh port dies with it
const hostPID = 1

// owned tells a leftover of ours from a link the operator keeps under the same name
const owned = "trainshard"

const ruleset = "trainshard"

// every write goes through the fd checked here: re-opening by pid would undo the check
func sandbox(pid int) (ns.NsHandle, error) {
	target, err := ns.GetFromPid(pid)
	if err != nil {
		return ns.None(), fmt.Errorf("sandbox %d namespace: %w", pid, err)
	}
	if err := checkConfined(pid, target); err != nil {
		target.Close()
		return ns.None(), err
	}
	return target, nil
}

// the thread's namespace, not the process's: inNetns may have parked the main thread in a sandbox
func checkConfined(pid int, target ns.NsHandle) error {
	var held unix.Stat_t
	if err := unix.Fstat(int(target), &held); err != nil {
		return fmt.Errorf("sandbox %d namespace: %w", pid, err)
	}
	outside := make([]nsID, 0, 2)
	for _, path := range []string{fmt.Sprintf("/proc/%d/ns/net", hostPID), "/proc/thread-self/ns/net"} {
		var known unix.Stat_t
		if err := unix.Stat(path, &known); err != nil {
			return fmt.Errorf("namespace %s: %w", path, err)
		}
		outside = append(outside, nsID{dev: uint64(known.Dev), ino: uint64(known.Ino)})
	}
	return confined(pid, nsID{dev: uint64(held.Dev), ino: uint64(held.Ino)}, outside...)
}

func hostNS() (ns.NsHandle, error) {
	target, err := ns.GetFromPid(hostPID)
	if err != nil {
		return ns.None(), fmt.Errorf("host namespace: %w", err)
	}
	return target, nil
}

func handleAt(target ns.NsHandle, where string) (*netlink.Handle, error) {
	handle, err := netlink.NewHandleAt(target)
	if err != nil {
		return nil, fmt.Errorf("netlink in %s: %w", where, err)
	}
	return handle, nil
}

func hostHandle() (*netlink.Handle, error) {
	target, err := hostNS()
	if err != nil {
		return nil, err
	}
	defer target.Close()
	return handleAt(target, "the host")
}

// find answers an absent link with nil, not an error
func find(handle *netlink.Handle, device, where string) (netlink.Link, error) {
	link, err := handle.LinkByName(device)
	var absent netlink.LinkNotFoundError
	if errors.As(err, &absent) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look for %s in %s: %w", device, where, err)
	}
	return link, nil
}

func lookup(pid int, device string) (*netlink.Handle, netlink.Link, error) {
	target, err := sandbox(pid)
	if err != nil {
		return nil, nil, err
	}
	defer target.Close()

	where := fmt.Sprintf("sandbox %d", pid)
	handle, err := handleAt(target, where)
	if err != nil {
		return nil, nil, err
	}
	link, err := find(handle, device, where)
	if err != nil || link == nil {
		handle.Close()
		return nil, nil, err
	}
	return handle, link, nil
}

func present(pid int, device string) (bool, error) {
	handle, link, err := lookup(pid, device)
	if err != nil || link == nil {
		return false, err
	}
	handle.Close()
	return true, nil
}

// holds wants exactly these peers, so a list the coordinator has since changed reads as not up
func holds(pid int, device, own string, want wgtypes.Config) (bool, error) {
	handle, link, err := lookup(pid, device)
	if err != nil || link == nil {
		return false, err
	}
	defer handle.Close()

	if link.Attrs().Flags&net.FlagUp == 0 {
		return false, nil
	}
	addrs, err := handle.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return false, fmt.Errorf("addresses on %s in sandbox %d: %w", device, pid, err)
	}
	if !slices.ContainsFunc(addrs, func(a netlink.Addr) bool { return a.IP.String() == own }) {
		return false, nil
	}

	var found *wgtypes.Device
	if err := withWG(pid, func(wg *wgctrl.Client) error {
		found, err = wg.Device(device)
		return err
	}); err != nil {
		return false, err
	}
	return samePeers(found.Peers, want.Peers), nil
}

func remove(pid int, device string) error {
	handle, link, err := lookup(pid, device)
	if err != nil || link == nil {
		return err
	}
	defer handle.Close()

	if err := handle.LinkDel(link); err != nil {
		return fmt.Errorf("delete %s in sandbox %d: %w", device, pid, err)
	}
	return nil
}

// raise addresses the link from outside the namespace: a netlink socket can be opened onto
// another namespace, which a wireguard socket cannot
func raise(pid int, device, own string) error {
	handle, link, err := lookup(pid, device)
	if err != nil {
		return err
	}
	if link == nil {
		return fmt.Errorf("%s is not in sandbox %d", device, pid)
	}
	defer handle.Close()

	addr, err := netlink.ParseAddr(own + "/16")
	if err != nil {
		return err
	}
	if err := handle.AddrReplace(link, addr); err != nil {
		return fmt.Errorf("address %s on %s: %w", own, device, err)
	}
	// a rank this node held before now belongs to another node, so its address goes
	held, err := handle.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("addresses on %s: %w", device, err)
	}
	for _, other := range held {
		if other.IP.Equal(addr.IP) {
			continue
		}
		if err := handle.AddrDel(link, &other); err != nil {
			return fmt.Errorf("drop %s from %s: %w", other.IP, device, err)
		}
	}
	if err := handle.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring %s up: %w", device, err)
	}
	return nil
}

// build adds the link in the host's namespace on purpose: a wireguard socket stays in the
// namespace the link was created in, so the mesh port is the host's and outlives this daemon
func build(device string, cfg wgtypes.Config, pid int) error {
	if err := discard(device); err != nil {
		return err
	}
	target, err := sandbox(pid)
	if err != nil {
		return err
	}
	defer target.Close()
	root, err := hostNS()
	if err != nil {
		return err
	}
	defer root.Close()
	host, err := handleAt(root, "the host")
	if err != nil {
		return err
	}
	defer host.Close()

	link := &netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: device, Alias: owned}}
	if err := host.LinkAdd(link); err != nil {
		return fmt.Errorf("add %s: %w", device, err)
	}

	// until the move lands the link is the host's, where Remove does not look and a leftover
	// blocks every later add
	if err := configure(root, host, link, cfg, pid, target); err != nil {
		if cleanup := host.LinkDel(link); cleanup != nil {
			return errors.Join(err, fmt.Errorf("delete %s after a failed setup: %w", device, cleanup))
		}
		return err
	}
	return nil
}

func configure(root ns.NsHandle, host *netlink.Handle, link netlink.Link, cfg wgtypes.Config, pid int, target ns.NsHandle) error {
	device := link.Attrs().Name

	if err := wgIn(root, func(wg *wgctrl.Client) error {
		return wg.ConfigureDevice(device, cfg)
	}); err != nil {
		return fmt.Errorf("configure %s: %w", device, err)
	}
	if err := host.LinkSetNsFd(link, int(target)); err != nil {
		return fmt.Errorf("move %s into sandbox %d: %w", device, pid, err)
	}
	return nil
}

// discard clears a link of ours on the host, which can only be a setup that died mid-way: a live
// one is in a sandbox. A link that is not ours stays, and the node is refused instead
func discard(device string) error {
	handle, err := hostHandle()
	if err != nil {
		return err
	}
	defer handle.Close()
	link, err := find(handle, device, "the host")
	if err != nil || link == nil {
		return err
	}

	if link.Type() != "wireguard" || link.Attrs().Alias != owned {
		return fmt.Errorf("%s on the host is a %s this daemon did not create, so it stays; rename it to free the slot: %w", device, link.Type(), errForeignLink)
	}
	if err := handle.LinkDel(link); err != nil {
		return fmt.Errorf("delete leftover %s: %w", device, err)
	}
	return nil
}

// listen binds in the host's namespace because that is where the wireguard socket will live
func listen(ctx context.Context, port int) error {
	root, err := hostNS()
	if err != nil {
		return err
	}
	defer root.Close()

	return inNetns(root, func() error {
		var open net.ListenConfig
		socket, err := open.ListenPacket(ctx, "udp", net.JoinHostPort("", strconv.Itoa(port)))
		if err != nil {
			return err
		}
		return socket.Close()
	})
}

// inNetns parks a thread in another namespace because a wireguard or udp socket opens where its
// thread stands. A thread that cannot be moved back stays locked, so it dies with the goroutine
// instead of serving other work in the wrong namespace
func inNetns(target ns.NsHandle, fn func() error) error {
	done := make(chan error, 1)

	go func() {
		runtime.LockOSThread()

		back, err := enter(target)
		if err != nil {
			runtime.UnlockOSThread()
			done <- err
			return
		}

		err = fn()
		if restored := back(); restored != nil {
			done <- errors.Join(err, restored)
			return
		}

		runtime.UnlockOSThread()
		done <- err
	}()

	return <-done
}

func withWG(pid int, fn func(*wgctrl.Client) error) error {
	target, err := sandbox(pid)
	if err != nil {
		return err
	}
	defer target.Close()

	return wgIn(target, fn)
}

func wgIn(target ns.NsHandle, fn func(*wgctrl.Client) error) error {
	return inNetns(target, func() error {
		wg, err := wgctrl.New()
		if err != nil {
			return err
		}
		defer wg.Close()

		return fn(wg)
	})
}

func enter(target ns.NsHandle) (func() error, error) {
	own, err := ns.Get()
	if err != nil {
		return nil, err
	}
	if err := ns.Set(target); err != nil {
		own.Close()
		return nil, fmt.Errorf("enter namespace: %w", err)
	}

	return func() error {
		defer own.Close()
		return ns.Set(own)
	}, nil
}

// fenced is false for a sandbox docker started again, which comes back with a fresh namespace
func fenced(pid int) (bool, error) {
	target, err := sandbox(pid)
	if err != nil {
		return false, err
	}
	defer target.Close()

	conn, err := nftables.New(nftables.WithNetNSFd(int(target)))
	if err != nil {
		return false, fmt.Errorf("nftables on sandbox %d: %w", pid, err)
	}
	defer conn.CloseLasting()

	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return false, fmt.Errorf("list tables in sandbox %d: %w", pid, err)
	}
	return slices.ContainsFunc(tables, func(t *nftables.Table) bool { return t.Name == ruleset }), nil
}

// fence replaces the sandbox's whole ruleset in one netlink transaction: a half-applied
// firewall would either strand the run or leak it onto the operator's network
func fence(pid int, device string, denied []string, allowed []allowance) error {
	target, err := sandbox(pid)
	if err != nil {
		return err
	}
	defer target.Close()

	conn, err := nftables.New(nftables.WithNetNSFd(int(target)))
	if err != nil {
		return fmt.Errorf("nftables on sandbox %d: %w", pid, err)
	}
	defer conn.CloseLasting()

	conn.FlushRuleset()

	drop := nftables.ChainPolicyDrop
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: ruleset})
	input := conn.AddChain(&nftables.Chain{
		Name:     "input",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookInput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &drop,
	})
	output := conn.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &drop,
	})

	// the interface accepts stay above the denied ranges: mesh addresses sit inside 10.0.0.0/8, which
	// is denied by default, so a deny read first would drop the training's own traffic to its peers
	for _, side := range []struct {
		chain *nftables.Chain
		key   expr.MetaKey
	}{{input, expr.MetaKeyIIFNAME}, {output, expr.MetaKeyOIFNAME}} {
		conn.AddRule(&nftables.Rule{Table: table, Chain: side.chain, Exprs: onInterface(side.key, "lo")})
		conn.AddRule(&nftables.Rule{Table: table, Chain: side.chain, Exprs: onInterface(side.key, device)})
		conn.AddRule(&nftables.Rule{Table: table, Chain: side.chain, Exprs: established()})
	}

	// denied ranges are read before the sources a run asked for, so a source that resolves into the
	// operator's own network is dropped rather than opened
	for _, cidr := range denied {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("denied cidr %q: %w", cidr, err)
		}
		conn.AddRule(&nftables.Rule{Table: table, Chain: output, Exprs: toDestination(*network, 0, expr.VerdictDrop)})
	}

	for _, entry := range allowed {
		conn.AddRule(&nftables.Rule{Table: table, Chain: output, Exprs: toDestination(single(entry.address), entry.port, expr.VerdictAccept)})
	}

	if err := conn.Flush(); err != nil {
		return fmt.Errorf("apply ruleset in sandbox %d: %w", pid, err)
	}
	return nil
}

func onInterface(key expr.MetaKey, device string) []expr.Any {
	padded := make([]byte, unix.IFNAMSIZ)
	copy(padded, device)

	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: padded},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

func established() []expr.Any {
	return []expr.Any{
		&expr.Ct{Key: expr.CtKeySTATE, Register: 1},
		&expr.Bitwise{
			SourceRegister: 1,
			DestRegister:   1,
			Len:            4,
			Mask:           binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED),
			Xor:            binaryutil.NativeEndian.PutUint32(0),
		},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}
}

// toDestination matches the destination address, and a tcp port when one is given. The family has
// to be pinned first: in an inet table the same offset reads a different header for v4 and v6
func toDestination(network net.IPNet, port int, verdict expr.VerdictKind) []expr.Any {
	offset, length, family := uint32(16), uint32(4), byte(unix.NFPROTO_IPV4)
	address, mask := network.IP.To4(), net.IP(network.Mask).To4()
	if address == nil {
		offset, length, family = 24, 16, unix.NFPROTO_IPV6
		address, mask = network.IP.To16(), net.IP(network.Mask).To16()
	}

	exprs := []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{family}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: length},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: length, Mask: mask, Xor: make([]byte, length)},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: address.Mask(network.Mask)},
	}
	if port > 0 {
		exprs = append(exprs,
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(port))},
		)
	}
	return append(exprs, &expr.Verdict{Kind: verdict})
}

func single(address net.IP) net.IPNet {
	if v4 := address.To4(); v4 != nil {
		return net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
	}
	return net.IPNet{IP: address.To16(), Mask: net.CIDRMask(128, 128)}
}
