package transport_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/transport"
	"devshard/transport/rpcserver"
	"devshard/types"
)

type retryRPCLookup struct {
	core retryRPCCore
}

func (l retryRPCLookup) SessionServerExisting(string) (rpcserver.SessionCore, error) {
	return l.core, nil
}
func (l retryRPCLookup) SessionForParticipant(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}
func (l retryRPCLookup) SessionForOwner(id, addr string) (rpcserver.SessionCore, error) {
	_ = addr
	return l.SessionServerExisting(id)
}
func (l retryRPCLookup) SessionForStartProof(id, addr string, _ []types.Diff, _ string) (rpcserver.SessionCore, error) {
	return l.SessionForParticipant(id, addr)
}

type retryRPCCore struct {
	stateHits     *atomic.Int32
	challengeHits *atomic.Int32
	nonce         uint64
	root          []byte
}

func (c retryRPCCore) ServeGetSignatures(uint64) (map[uint32][]byte, error) {
	return map[uint32][]byte{}, nil
}
func (c retryRPCCore) AllowsSender(string) bool  { return true }
func (c retryRPCCore) IsGroupMember(string) bool { return true }
func (c retryRPCCore) ServeGetState() (uint64, []byte, error) {
	if c.stateHits != nil {
		c.stateHits.Add(1)
	}
	return c.nonce, append([]byte(nil), c.root...), nil
}
func (c retryRPCCore) ServeChallengeReceipt(context.Context, transport.ChallengeReceiptRequest) (*transport.ChallengeReceiptResponse, error) {
	if c.challengeHits != nil {
		c.challengeHits.Add(1)
	}
	return &transport.ChallengeReceiptResponse{}, nil
}

func TestRefusalRetryUsesThePeerRPC(t *testing.T) {
	root := bytes.Repeat([]byte{7}, 32)
	var stateHits, challengeHits atomic.Int32
	rpc := newLargeRPCClient(t, retryRPCLookup{core: retryRPCCore{
		stateHits: &stateHits, challengeHits: &challengeHits, nonce: 4, root: root,
	}}, transport.EndpointState+","+transport.EndpointChallengeReceipt, transport.DefaultClientConfig())
	t.Cleanup(rpc.Close)

	var tip host.SessionTip = rpc
	var exec host.ExecutorClient = rpc
	nonce, got, err := tip.SessionHead(context.Background())
	require.NoError(t, err)
	require.Equal(t, uint64(4), nonce)
	require.Equal(t, root, got)
	_, _, err = exec.ChallengeReceipt(context.Background(), 1, &host.InferencePayload{Prompt: []byte("p")}, nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), stateHits.Load())
	require.Equal(t, int32(1), challengeHits.Load())
}
