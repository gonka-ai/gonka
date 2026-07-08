package mlnode

import (
	"context"
	"errors"
	"fmt"

	"devshard/nodemanager/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Sentinel errors returned by Acquire. Callers should use errors.Is / the
// Is* helpers rather than matching error strings.
//
//	ErrNoNodesAvailable — dapi is up but has no free node for the model
//	                      (stay on the gRPC path; do not fall back).
//	ErrUnavailable      — node-manager is unreachable (dapi down / transport);
//	                      fall back to the local ML-node cache.
var (
	ErrNoNodesAvailable = errors.New("nodemanager: no nodes available")
	ErrUnavailable      = errors.New("nodemanager: unavailable")
)

// Client is a gRPC client for the node-manager NodeManager service.
type Client struct {
	conn   *grpc.ClientConn
	client gen.NodeManagerClient
}

// NewClient dials node-manager at addr and returns a Client.
// The connection uses insecure credentials — TLS is terminated at the network layer.
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("nodemanager: dial %s: %w", addr, err)
	}
	return &Client{conn: conn, client: gen.NewNodeManagerClient(conn)}, nil
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// NodeManagerClient returns the underlying gRPC stub for sharing with runtimeconfig.
func (c *Client) NodeManagerClient() gen.NodeManagerClient {
	return c.client
}

// ClientForTest wires an existing NodeManagerClient without owning a connection.
// conn.Close is a no-op when conn is nil.
func ClientForTest(client gen.NodeManagerClient) *Client {
	return &Client{client: client}
}

// Acquire reserves an available ML node for the given model.
// excludedNodeIDs contains node IDs that failed earlier in the same retry loop.
//
// On failure the returned error wraps a sentinel and preserves the gRPC status
// code so callers can branch with IsNoNodesAvailable / IsUnavailable (or
// errors.Is / status.Code).
func (c *Client) Acquire(ctx context.Context, model string, excludedNodeIDs []string) (*gen.AcquireMLNodeResponse, error) {
	resp, err := c.client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{
		Model:         model,
		ExcludedNodes: excludedNodeIDs,
	})
	if err != nil {
		return nil, classifyAcquireError(model, err)
	}
	return resp, nil
}

// classifyAcquireError maps a gRPC Acquire failure onto a sentinel while
// keeping the original status error in the chain (status.Code still works).
func classifyAcquireError(model string, err error) error {
	switch status.Code(err) {
	case codes.ResourceExhausted:
		return fmt.Errorf("%w for model %q: %w", ErrNoNodesAvailable, model, err)
	case codes.Unavailable:
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	default:
		// Non-status transport failures (e.g. connection errors before a
		// status is attached) are treated as unavailable so callers can fall back.
		if _, ok := status.FromError(err); !ok {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return fmt.Errorf("nodemanager: acquire: %w", err)
	}
}

// IsNoNodesAvailable reports whether err means dapi is reachable but has no
// free node for the requested model. Callers should stay on the gRPC path.
func IsNoNodesAvailable(err error) bool {
	return errors.Is(err, ErrNoNodesAvailable) || status.Code(err) == codes.ResourceExhausted
}

// IsUnavailable reports whether err means the node-manager is unreachable
// (dapi down or transport failure). Callers should fall back to the local cache.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrUnavailable) || status.Code(err) == codes.Unavailable
}

// Release reports the outcome of a completed inference to node-manager.
func (c *Client) Release(ctx context.Context, lockID string, outcome gen.ReleaseOutcome) error {
	_, err := c.client.ReleaseMLNode(ctx, &gen.ReleaseMLNodeRequest{
		LockId:  lockID,
		Outcome: outcome,
	})
	if err != nil {
		return fmt.Errorf("nodemanager: release %s: %w", lockID, err)
	}
	return nil
}
