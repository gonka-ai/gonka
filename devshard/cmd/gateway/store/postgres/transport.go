package postgres

import (
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func requireTLSForRemoteHosts(cfg *pgxpool.Config) error {
	primary := cfg.ConnConfig
	if !isLoopbackHost(primary.Host) && primary.TLSConfig == nil {
		return fmt.Errorf("%w: host %s", ErrPlaintextRemoteHost, primary.Host)
	}
	for _, fallback := range primary.Fallbacks {
		if !isLoopbackHost(fallback.Host) && fallback.TLSConfig == nil {
			return fmt.Errorf("%w: host %s", ErrPlaintextRemoteHost, fallback.Host)
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.HasPrefix(host, unixSocketHostPrefix) || strings.EqualFold(host, loopbackHostName) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
