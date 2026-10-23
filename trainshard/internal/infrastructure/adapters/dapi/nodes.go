package dapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

const pathNodes = "/admin/v1/nodes"

type node struct {
	Node struct {
		ID string `json:"id"`
	} `json:"node"`
	State struct {
		CurrentStatus string `json:"current_status"`
		LockCount     int    `json:"lock_count"`
		AdminState    struct {
			Enabled bool `json:"enabled"`
			Stopped bool `json:"stopped"`
		} `json:"admin_state"`
	} `json:"state"`
}

func (c *Client) Drained(ctx context.Context, ref vo.NodeRef) (bool, error) {
	held, err := c.node(ctx, ref)
	if err != nil {
		return false, err
	}
	return drained(held), nil
}

func (c *Client) Drain(ctx context.Context, ref vo.NodeRef) (bool, error) {
	held, err := c.node(ctx, ref)
	if err != nil {
		return false, err
	}
	if held.State.AdminState.Enabled {
		if err := c.nodeAction(ctx, ref, "disable"); err != nil {
			return false, err
		}
	}
	if !held.State.AdminState.Stopped {
		if err := c.nodeAction(ctx, ref, "stop"); err != nil {
			return false, err
		}
	}
	return c.Drained(ctx, ref)
}

func (c *Client) Return(ctx context.Context, ref vo.NodeRef) error {
	held, err := c.node(ctx, ref)
	if err != nil {
		return err
	}
	if held.State.AdminState.Stopped {
		if err := c.nodeAction(ctx, ref, "start"); err != nil {
			return err
		}
	}
	if !held.State.AdminState.Enabled {
		return c.nodeAction(ctx, ref, "enable")
	}
	return nil
}

func (c *Client) nodeAction(ctx context.Context, ref vo.NodeRef, action string) error {
	return c.call(ctx, http.MethodPost, pathNodes+"/"+string(ref.NodeID)+"/"+action, nil, nil)
}

func (c *Client) node(ctx context.Context, ref vo.NodeRef) (node, error) {
	var held []node
	if err := c.call(ctx, http.MethodGet, pathNodes, nil, &held); err != nil {
		return node{}, err
	}
	for _, one := range held {
		if one.Node.ID == string(ref.NodeID) {
			return one, nil
		}
	}
	return node{}, shared.New("NODE_UNKNOWN", shared.ErrNotFound,
		fmt.Sprintf("the dapi serves no node %q: a training node is the node the dapi serves inference from, under the same id", ref.NodeID))
}

func drained(held node) bool {
	return !held.State.AdminState.Enabled &&
		held.State.AdminState.Stopped &&
		held.State.LockCount == 0 &&
		held.State.CurrentStatus == types.HardwareNodeStatus_STOPPED.String()
}
