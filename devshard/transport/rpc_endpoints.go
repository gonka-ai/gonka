package transport

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"devshard/logging"
)

// DEVSHARD_RPC_ENDPOINTS names (e.g. "gossip,diffs").
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
	EndpointPayload          = "payload"
)

// DefaultRPCMaxConnsPerPeer is MaxIdleConnsPerHost / MaxConnsPerHost on a
// PeerConn. Matches DefaultRPCMaxStreams so Watch + concurrent Chats are
// not queued behind the HTTP/1.1 pool (finding 5). Advertised max_streams
// is still min(MaxStreams, MaxConns) if either env is lowered.
const DefaultRPCMaxConnsPerPeer = 256

// HostRPCEscrowID is the URL escrow for Watch and live-session Attach
// renewals. It is not a real escrow: the path keeps /sessions/:id/rpc/ so
// versiond still matches the existing route shape. First Attach uses the
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
// intersects this list. RPCClient.Uses is the same gate: an opted-in name
// that is not on this list stays HTTP.
//
// Add a name here when that method actually calls Connect.
var attachRPCEndpoints = []string{
	EndpointChat,
	EndpointSignatures,
	EndpointMempool,
	EndpointDiffs,
	EndpointGossip,
	EndpointRepair,
	EndpointSeed,
	EndpointVerifyTimeout,
	EndpointVerifyErrorMiss,
	EndpointChallengeReceipt,
	EndpointPayload,
}

var knownRPCEndpoints = map[string]struct{}{
	EndpointChat:             {},
	EndpointGossip:           {},
	EndpointDiffs:            {},
	EndpointSignatures:       {},
	EndpointMempool:          {},
	EndpointSeed:             {},
	EndpointRepair:           {},
	EndpointVerifyTimeout:    {},
	EndpointVerifyErrorMiss:  {},
	EndpointChallengeReceipt: {},
	EndpointPayload:          {},
}

var unwiredRPCWarnOnce sync.Once

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
	set := ParseRPCEndpoints(os.Getenv(envRPCEndpoints))
	warnUnwiredRPCEndpoints(set)
	return set
}

func isAttachRPCEndpoint(name string) bool {
	for _, n := range attachRPCEndpoints {
		if n == name {
			return true
		}
	}
	return false
}

// classifyUnwiredRPCEndpoints splits opt-in names that are not yet on
// Connect (known names not in attachRPCEndpoints) from typos.
func classifyUnwiredRPCEndpoints(s EndpointSet) (unwired, unknown []string) {
	for name := range s {
		if isAttachRPCEndpoint(name) {
			continue
		}
		if _, ok := knownRPCEndpoints[name]; ok {
			unwired = append(unwired, name)
		} else {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unwired)
	sort.Strings(unknown)
	return unwired, unknown
}

func warnUnwiredRPCEndpoints(s EndpointSet) {
	unwired, unknown := classifyUnwiredRPCEndpoints(s)
	if len(unwired) == 0 && len(unknown) == 0 {
		return
	}
	unwiredRPCWarnOnce.Do(func() {
		logging.Warn("DEVSHARD_RPC_ENDPOINTS names are not served over Connect; those methods stay HTTP",
			"subsystem", "transport",
			"unwired", strings.Join(unwired, ","),
			"unknown", strings.Join(unknown, ","),
			"wired", strings.Join(attachRPCEndpoints, ","),
		)
	})
}

func resetUnwiredRPCWarnForTest() {
	unwiredRPCWarnOnce = sync.Once{}
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
// host session token (empty set, typos, known names not yet wired) or
// when hostAddress is empty (never share "@version"). Chat is on
// attachRPCEndpoints: opt-in `chat` starts Attach. Otherwise it returns
// an *RPCClient and starts the PeerConn attach loop.
func SelectTransport(httpClient *HTTPClient, hostAddress string, endpoints EndpointSet, extra *ClientConfig) any {
	warnUnwiredRPCEndpoints(endpoints)
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
	base := httpClient.BaseURL()
	dial, err := PeerRPCDialSetFromEnv(base)
	if err != nil {
		logging.Warn("DEVSHARD_RPC_H2_* ignored; HTTP/1.1 on InferenceUrl",
			"subsystem", "transport",
			"error", err,
		)
		dial = PeerRPCDialSet{InferenceURL: base}
	}
	grpc := RPCH2GRPCEnabled(os.Getenv(envRPCH2GRPC)) && dial.H2URL != ""
	return PeerConnConfig{
		BaseURL:      base,
		RoutePrefix:  httpClient.RoutePrefix(),
		DoorEscrowID: httpClient.escrowID,
		HostAddress:  strings.TrimSpace(hostAddress),
		Signer:       httpClient.signer,
		MaxConns:     maxConns,
		Adoption:     adoption,
		DialSet:      dial,
		GRPC:         grpc,
	}
}
