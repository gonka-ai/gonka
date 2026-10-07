//go:build testenvci

package citest

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"common/httpguard"
	"connectrpc.com/connect"

	"devshard/signing"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/transport"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// TestPeerRPCParity is §8.4. One overlay stack. The gateway chat is the
// same prompt on Connect over HTTP/2 and on native gRPC. Session HTTP is
// retired, so clearing DEVSHARD_RPC_ENDPOINTS does not select a JSON chat
// leg, and upgrade off would send Connect to :8080, which the router 404s.
// GetSignatures and GetDiffs round-trip on the HTTP client and on one
// native gRPC session. A second Watch for the Connect dial would be a
// third stream for this signer (the child allows two), so Connect
// unaries are not opened beside it. A lowered child budget then
// returns resource_exhausted on both HTTP/2 legs.
func TestPeerRPCParity(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	requireOverlayRPC(t)
	if os.Getenv(harness.EnvVersiondImage) != "" || os.Getenv(harness.EnvVersiondRouterImage) != "" {
		t.Fatal("parity is current versiond and versiond-router; unset TESTENV_VERSIOND_IMAGE and TESTENV_VERSIOND_ROUTER_IMAGE")
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DEVSHARD_RPC_GRPC")), "true") {
		t.Fatal("boot with DEVSHARD_RPC_GRPC unset; the test turns native gRPC on for leg P2")
	}
	harness.RequireDocker(t)
	httpguard.SetAllowPrivate(true)

	stack, cfg, eps := harness.BootProxyOverlayStack(t, "citest-peerrpc-parity-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "proxy", "versiond-0", "versiond-1", "versiond-router", "mock-openai")
		}
	})
	version := cfg.Versiond.VersionName
	if version == "" {
		version = "v2"
	}
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	beforeP1 := chatOKCount(t, stack, cfg)
	p1 := captureGatewayChat(t, client, eps, cfg, "citest parity")
	require.Greater(t, chatOKCount(t, stack, cfg), beforeP1, "Connect HTTP/2 chat must be counted on the child")

	eps = switchGatewayLeg(t, stack, cfg, client, "true", "true", peerRPCEndpoints)
	beforeP2 := chatOKCount(t, stack, cfg)
	p2 := captureGatewayChat(t, client, eps, cfg, "citest parity")
	require.Greater(t, chatOKCount(t, stack, cfg), beforeP2, "native gRPC chat must be counted on the child")

	require.Equal(t, p1.full, p2.full)
	require.Equal(t, p1.trunc, p2.trunc)
	require.NotEmpty(t, p1.full.content)
	require.NotEmpty(t, p1.trunc.content)
	require.NotEmpty(t, p1.trunc.finish)

	compareUnaries(t, stack, cfg, eps, version)
	requireResourceExhaustedOnH2(t, stack, cfg, eps, version)
}

type chatSnap struct {
	content string
	finish  string
	stream  string
	done    bool
}

type chatPair struct {
	full  chatSnap
	trunc chatSnap
}

func captureGatewayChat(t *testing.T, client *http.Client, eps harness.Endpoints, cfg *config.File, prompt string) chatPair {
	t.Helper()
	model := config.PrimaryModelID(cfg)
	fullReq := harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: prompt}},
		MaxTokens: 32,
	}
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, fullReq)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	stream, done := harness.PostGatewayChatCompletionStream(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, fullReq)
	require.True(t, done, "stream must end with [DONE]")
	require.Equal(t, resp.Choices[0].Message.Content, stream)

	truncReq := fullReq
	truncReq.MaxTokens = 1
	truncResp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, truncReq)
	return chatPair{
		full: chatSnap{
			content: resp.Choices[0].Message.Content,
			finish:  resp.Choices[0].FinishReason,
			stream:  stream,
			done:    done,
		},
		trunc: chatSnap{
			content: truncResp.Choices[0].Message.Content,
			finish:  truncResp.Choices[0].FinishReason,
		},
	}
}

func switchGatewayLeg(t *testing.T, stack *harness.Stack, cfg *config.File, client *http.Client, upgrade, grpc, endpoints string) harness.Endpoints {
	t.Helper()
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_ENDPOINTS", strconv.Quote(endpoints))
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_H2_UPGRADE", strconv.Quote(upgrade))
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_GRPC", strconv.Quote(grpc))
	stack.RecreateServices(t, "devshardctl")
	eps := stack.Endpoints(t, cfg)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	requirePrintenv(t, stack, "DEVSHARD_RPC_H2_UPGRADE", upgrade)
	requirePrintenv(t, stack, "DEVSHARD_RPC_GRPC", grpc)
	requirePrintenv(t, stack, "DEVSHARD_RPC_ENDPOINTS", endpoints)
	return eps
}

func requirePrintenv(t *testing.T, stack *harness.Stack, key, want string) {
	t.Helper()
	out, err := stack.ComposeExecOutput("devshardctl", "printenv", key)
	require.NoError(t, err)
	require.Equal(t, want, strings.TrimSpace(out))
}

func compareUnaries(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, version string) {
	t.Helper()
	// The gateway's PeerConn already holds a Watch for this signer, and the
	// child allows two. Chat comparison is done. Stop the gateway, then
	// recreate the children so a stream the proxy is still holding does
	// not keep that slot. The unary dial is the only Watch for this signer.
	stack.StopService(t, "devshardctl")
	var hosts []string
	for _, h := range cfg.Hosts {
		hosts = append(hosts, h.ID)
	}
	stack.RecreateServices(t, hosts...)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health after dropping gateway watches", stack)
	escrow := config.PrimaryEscrowID(cfg)
	signer := userSigner(t, cfg)
	base := eps.RouterHTTP
	proxy := stack.ProxyH2HTTP(t)
	_, rpc := attachablePeer(t, base, proxy, escrow, version, signer, cfg, true)
	defer rpc.Close()

	httpClient := transport.NewHTTPClient(base, escrow, signer, clientConfig(version, 0))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpSigs, err := httpClient.GetSignatures(ctx, 1)
	require.NoError(t, err)
	httpDiffs, err := httpClient.GetDiffs(ctx, 1, 20)
	require.NoError(t, err)

	sigs, diffs := peerUnaries(t, rpc, ctx)
	require.Equal(t, normSigs(httpSigs), normSigs(sigs))
	require.Equal(t, diffNonceSigs(httpDiffs), diffNonceSigs(diffs))
}

// peerUnaries reads both unaries once the Watch has published a token.
// A Watch refused at the two-stream cap clears that token immediately, so
// a call in that window is retried until the replacement Attach sticks.
func peerUnaries(t *testing.T, rpc *transport.RPCClient, ctx context.Context) (map[uint32][]byte, []types.Diff) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		if err := rpc.WaitReady(ctx); err != nil {
			last = err
		} else {
			sigs, err := rpc.GetSignatures(ctx, 1)
			diffs, errDiff := rpc.GetDiffs(ctx, 1, 20)
			if err == nil && errDiff == nil {
				return sigs, diffs
			}
			last = err
			if errDiff != nil {
				last = errDiff
			}
		}
		if !errors.Is(last, transport.ErrPeerNotReady) || time.Now().After(deadline) || ctx.Err() != nil {
			require.NoError(t, last)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func requireResourceExhaustedOnH2(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, version string) {
	t.Helper()
	t.Setenv("DEVSHARD_RPC_MSGS_PER_MIN", "10")
	harness.PatchComposeEnvKey(t, stack.ComposePath, "DEVSHARD_RPC_MSGS_PER_MIN", "10")
	var hosts []string
	for _, h := range cfg.Hosts {
		hosts = append(hosts, h.ID)
	}
	stack.RecreateServices(t, hosts...)
	harness.WaitGETOK(t, harness.GatewayChatClient(), eps.RouterHTTP+"/"+version+"/healthz", 5*time.Minute, "devshardd health after budget drop", stack)

	escrow := config.PrimaryEscrowID(cfg)
	signer := userSigner(t, cfg)
	base := eps.RouterHTTP
	proxy := stack.ProxyH2HTTP(t)
	// One Watch per protocol, both left open. Closing between them leaves
	// the stream counted, and the next dial is then a third Watch.
	hostAddr, connectRPC := attachablePeer(t, base, proxy, escrow, version, signer, cfg, false)
	warmCtx, warmCancel := context.WithTimeout(context.Background(), time.Minute)
	warmErr := peerSignatures(t, connectRPC, warmCtx)
	warmCancel()
	require.NoError(t, warmErr, "one GetSignatures must fit in the burst")

	grpcRPC := dialPeer(t, base, proxy, escrow, hostAddr, version, signer, true, 2*time.Second)
	for _, rpc := range []*transport.RPCClient{connectRPC, grpcRPC} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := peerSignatures(t, rpc, ctx)
		cancel()
		require.Error(t, err)
		require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), "%v", err)
	}
	grpcRPC.Close()
	connectRPC.Close()
}

// peerSignatures waits out a Watch-cap gap, then returns the first
// GetSignatures result that is not "session not ready".
func peerSignatures(t *testing.T, rpc *transport.RPCClient, ctx context.Context) error {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for {
		if err := rpc.WaitReady(ctx); err != nil {
			last = err
		} else {
			_, last = rpc.GetSignatures(ctx, 1)
			if !errors.Is(last, transport.ErrPeerNotReady) {
				return last
			}
		}
		if !errors.Is(last, transport.ErrPeerNotReady) || time.Now().After(deadline) || ctx.Err() != nil {
			return last
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func clientConfig(version string, queryTimeout time.Duration) transport.ClientConfig {
	cfg := transport.DefaultClientConfig()
	cfg.RoutePrefix = "/devshard/" + version
	if queryTimeout > 0 {
		cfg.QueryTimeout = queryTimeout
	}
	return cfg
}

func dialPeer(t *testing.T, base, proxy, escrow, hostAddr, version string, signer signing.Signer, grpc bool, queryTimeout time.Duration) *transport.RPCClient {
	t.Helper()
	hc := transport.NewHTTPClient(base, escrow, signer, clientConfig(version, queryTimeout))
	pc := transport.NewPeerConn(transport.PeerConnConfig{
		BaseURL:      base,
		RoutePrefix:  "/devshard/" + version,
		DoorEscrowID: escrow,
		HostAddress:  hostAddr,
		Signer:       signer,
		DialSet: transport.PeerRPCDialSet{
			InferenceURL: base,
			H2URL:        proxy,
		},
		GRPC: grpc,
	})
	rpc := transport.NewRPCClient(hc, pc, transport.ParseRPCEndpoints(peerRPCEndpoints))
	pc.Start()
	return rpc
}

func userSigner(t *testing.T, cfg *config.File) signing.Signer {
	t.Helper()
	signer, err := signing.SignerFromHex(cfg.User.PrivateKeyHex)
	require.NoError(t, err)
	return signer
}

// attachablePeer is a live session on the child whose Attach accepts this
// signer. The escrow hash picks one versiond; the other address is rejected.
// The caller Closes the client. Closing inside the probe and dialing again
// stacks a Watch on top of the gateway's stream for the same signer.
func attachablePeer(t *testing.T, base, proxy, escrow, version string, signer signing.Signer, cfg *config.File, grpc bool) (string, *transport.RPCClient) {
	t.Helper()
	var errs []string
	for _, h := range cfg.Hosts {
		rpc := dialPeer(t, base, proxy, escrow, h.Address, version, signer, grpc, 0)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		err := rpc.WaitReady(ctx)
		cancel()
		if err == nil {
			return h.Address, rpc
		}
		rpc.Close()
		errs = append(errs, h.ID+": "+err.Error())
	}
	t.Fatalf("no host accepted Attach: %s", strings.Join(errs, "; "))
	return "", nil
}

func normSigs(in map[uint32][]byte) map[uint32][]byte {
	if in == nil {
		return map[uint32][]byte{}
	}
	return in
}

type nonceSig struct {
	nonce uint64
	sig   string
}

func diffNonceSigs(diffs []types.Diff) []nonceSig {
	out := make([]nonceSig, len(diffs))
	for i, d := range diffs {
		out[i] = nonceSig{nonce: d.Nonce, sig: string(d.UserSig)}
	}
	return out
}

func chatOKCount(t *testing.T, stack *harness.Stack, cfg *config.File) float64 {
	t.Helper()
	var sum float64
	for _, body := range hostMetricBodies(t, stack, cfg) {
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if !strings.Contains(line, "devshard_peer_rpc_requests_total") || !strings.Contains(line, `endpoint="Chat"`) || !strings.Contains(line, `result="ok"`) {
				continue
			}
			fields := strings.Fields(line)
			v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
			require.NoError(t, err)
			sum += v
		}
	}
	return sum
}
