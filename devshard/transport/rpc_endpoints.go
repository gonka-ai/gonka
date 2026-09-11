package transport

import (
	"os"
	"strconv"
	"strings"
)

// DEVSHARD_RPC_ENDPOINTS names, matching the phase-2 flag (e.g. "gossip,diffs").
const (
	EndpointChat             = "chat"
	EndpointGossip           = "gossip"
	EndpointDiffs            = "diffs"
	EndpointSignatures       = "signatures"
	EndpointMempool          = "mempool"
	EndpointSeed             = "height-sync"
	EndpointRepair           = "repair"
	EndpointVerifyTimeout    = "verify-timeout"
	EndpointVerifyErrorMiss  = "verify-error-miss"
	EndpointChallengeReceipt = "challenge-receipt"
)

// DefaultRPCMaxConnsPerPeer is MaxIdleConnsPerHost / MaxConnsPerHost on a
// PeerConn. Raised from HTTPClient's 4 so Watch + queries are not serialized
// behind a long Chat.
const DefaultRPCMaxConnsPerPeer = 16

// HostRPCEscrowID is the URL escrow for Watch and live-session Attach
// renewals. It is not a real escrow: the path keeps /sessions/:id/rpc/ so
// versiond still matches the Phase 1–5 route shape. First Attach uses the
// door escrow (AllowsSender). After that the token is the session.
const HostRPCEscrowID = "_"

const (
	envRPCEndpoints       = "DEVSHARD_RPC_ENDPOINTS"
	envRPCMaxConnsPerPeer = "DEVSHARD_RPC_MAX_CONNS_PER_PEER"
)

// EndpointSet is the DEVSHARD_RPC_ENDPOINTS opt-in set. Empty means HTTP
// everywhere.
type EndpointSet map[string]struct{}

func (s EndpointSet) Empty() bool {
	return len(s) == 0
}

func (s EndpointSet) Has(name string) bool {
	if len(s) == 0 {
		return false
	}
	_, ok := s[name]
	return ok
}

// attachRPCEndpoints are names whose client path currently sends
// X-Devshard-Session. SelectTransport starts PeerConn only if the opt-in set
// intersects this list (finding 5).
//
// Phase 3: add gossip and the other migrated unaries when those methods
// call Connect. Phase 5: add EndpointChat when Send leaves HTTP — revisit
// this list then; chat must start Attach once it uses the host token.
var attachRPCEndpoints = []string{EndpointSignatures}

// NeedsAttach is whether this set includes a method that currently needs
// a host session token. Unknown names are kept in the set but do not
// start Attach.
func (s EndpointSet) NeedsAttach() bool {
	for _, name := range attachRPCEndpoints {
		if s.Has(name) {
			return true
		}
	}
	return false
}

// ParseRPCEndpoints parses a comma-separated endpoint list. Unknown names
// are kept so a future name is not silently dropped.
func ParseRPCEndpoints(raw string) EndpointSet {
	set := EndpointSet{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		set[name] = struct{}{}
	}
	return set
}

// RPCEndpointsFromEnv reads DEVSHARD_RPC_ENDPOINTS. Empty / unset is HTTP.
func RPCEndpointsFromEnv() EndpointSet {
	return ParseRPCEndpoints(os.Getenv(envRPCEndpoints))
}

// RPCMaxConnsPerPeerFromEnv reads DEVSHARD_RPC_MAX_CONNS_PER_PEER.
func RPCMaxConnsPerPeerFromEnv() int {
	raw := strings.TrimSpace(os.Getenv(envRPCMaxConnsPerPeer))
	if raw == "" {
		return DefaultRPCMaxConnsPerPeer
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return DefaultRPCMaxConnsPerPeer
	}
	return n
}

// SelectTransport returns http unchanged when no opted-in name needs a
// host session token (empty set, chat-only, typos, Phase 3 names not yet
// wired) or when hostAddress is empty (finding 6: never share "@version").
// Otherwise it returns an *RPCClient and starts the PeerConn attach loop.
func SelectTransport(httpClient *HTTPClient, hostAddress string, endpoints EndpointSet, extra *ClientConfig) any {
	if httpClient == nil || !endpoints.NeedsAttach() {
		return httpClient
	}
	hostAddress = strings.TrimSpace(hostAddress)
	if hostAddress == "" {
		return httpClient
	}
	cfg := peerConnConfigFromClient(httpClient, hostAddress, extra)
	conn := acquirePeerConn(cfg)
	return NewRPCClient(httpClient, conn, endpoints)
}

func peerConnConfigFromClient(httpClient *HTTPClient, hostAddress string, extra *ClientConfig) PeerConnConfig {
	maxConns := RPCMaxConnsPerPeerFromEnv()
	var adoption *PeerRPCAdoption
	if extra != nil {
		if extra.RPCMaxConnsPerPeer > 0 {
			maxConns = extra.RPCMaxConnsPerPeer
		}
		adoption = extra.RPCAdoption
	}
	return PeerConnConfig{
		BaseURL:      httpClient.BaseURL(),
		RoutePrefix:  httpClient.RoutePrefix(),
		DoorEscrowID: httpClient.escrowID,
		HostAddress:  strings.TrimSpace(hostAddress),
		Signer:       httpClient.signer,
		MaxConns:     maxConns,
		Adoption:     adoption,
	}
}
