package mlnode

import (
	"context"
	"fmt"

	"devshard/nodemanager/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
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
func (c *Client) Acquire(ctx context.Context, model string, excludedNodeIDs []string) (*gen.AcquireMLNodeResponse, error) {
	resp, err := c.client.AcquireMLNode(ctx, &gen.AcquireMLNodeRequest{
		Model:         model,
		ExcludedNodes: excludedNodeIDs,
	})
	if err != nil {
		if code := status.Code(err); code == codes.ResourceExhausted {
			return nil, fmt.Errorf("nodemanager: no nodes available for model %q", model)
		}
		return nil, fmt.Errorf("nodemanager: acquire: %w", err)
	}
	return resp, nil
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
