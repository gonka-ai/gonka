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
	if err := c.call(ctx, http.MethodPost, pathNodes+"/"+string(ref.NodeID)+"/disable", nil, nil); err != nil {
		return false, err
	}
	return c.Drained(ctx, ref)
}

func (c *Client) Return(ctx context.Context, ref vo.NodeRef) error {
	return c.call(ctx, http.MethodPost, pathNodes+"/"+string(ref.NodeID)+"/enable", nil, nil)
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

// drained holds when the node is out of the way: disabled so no epoch gives it work, holding no lock
// from work already sent, and not in the middle of a proof of compute, which owns the cards outright.
// A disabled node stays up and idle rather than stopping — the dapi has no way to shut one down — so
// what finally clears the cards is the gpu check, not this
func drained(held node) bool {
	return !held.State.AdminState.Enabled &&
		held.State.LockCount == 0 &&
		held.State.CurrentStatus != types.HardwareNodeStatus_POC.String()
}
