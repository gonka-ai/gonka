package netns

import (
	"context"
	"fmt"
	"net"
	"strings"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

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
	if err := n.script(ctx, n.inside(pid, n.cfg.NFT, "-f", "-"), ruleset(iface(slot), n.cfg.DeniedCIDRs, allowed)); err != nil {
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

func ruleset(mesh string, denied []string, allowed []allowance) string {
	var out strings.Builder

	out.WriteString("table inet trainshard\n")
	out.WriteString("delete table inet trainshard\n")
	out.WriteString("table inet trainshard {\n")

	out.WriteString("\tchain input {\n")
	out.WriteString("\t\ttype filter hook input priority filter; policy drop;\n")
	out.WriteString("\t\tiif lo accept\n")
	fmt.Fprintf(&out, "\t\tiifname %q accept\n", mesh)
	out.WriteString("\t\tct state established,related accept\n")
	out.WriteString("\t}\n")

	out.WriteString("\tchain output {\n")
	out.WriteString("\t\ttype filter hook output priority filter; policy drop;\n")
	out.WriteString("\t\toif lo accept\n")
	fmt.Fprintf(&out, "\t\toifname %q accept\n", mesh)
	out.WriteString("\t\tct state established,related accept\n")
	for _, cidr := range denied {
		fmt.Fprintf(&out, "\t\t%s daddr %s drop\n", family(cidr), cidr)
	}
	for _, entry := range allowed {
		fmt.Fprintf(&out, "\t\t%s daddr %s tcp dport %d accept\n", family(entry.address.String()), entry.address, entry.port)
	}
	out.WriteString("\t}\n}\n")

	return out.String()
}

func family(address string) string {
	if strings.Contains(address, ":") {
		return "ip6"
	}
	return "ip"
}
