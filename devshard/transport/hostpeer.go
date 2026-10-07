package transport

import (
	"context"
	"time"

	"devshard/heightsync"
	"devshard/host"
	"devshard/signing"
)

// HostPeerClient is a slot's outbound client for repair probes and timeout
// verification. *HTTPClient and *RPCClient implement it.
type HostPeerClient interface {
	host.ExecutorClient
	HeightSyncRepair(ctx context.Context, req *heightsync.RepairRequest) (*heightsync.RepairResponse, error)
	// CloneWithSigner returns a client that signs as signer. HTTP clones
	// are a new client (no handshake). RPC clones keep the PeerConn; the
	// envelope signer must match the Attach peer (see RPCClient.cloneWithSigner).
	CloneWithSigner(signer signing.Signer, timeout time.Duration) HostPeerClient
	Close()
}

// HTTPPeerClients copies an HTTP-only roster into the HostPeerClient map.
func HTTPPeerClients(peers map[int]*HTTPClient) map[int]HostPeerClient {
	if peers == nil {
		return nil
	}
	out := make(map[int]HostPeerClient, len(peers))
	for k, v := range peers {
		out[k] = v
	}
	return out
}

func (c *HTTPClient) CloneWithSigner(signer signing.Signer, timeout time.Duration) HostPeerClient {
	return c.cloneWithSigner(signer, timeout)
}

func (c *RPCClient) CloneWithSigner(signer signing.Signer, timeout time.Duration) HostPeerClient {
	return c.cloneWithSigner(signer, timeout)
}

var (
	_ HostPeerClient = (*HTTPClient)(nil)
	_ HostPeerClient = (*RPCClient)(nil)
)
