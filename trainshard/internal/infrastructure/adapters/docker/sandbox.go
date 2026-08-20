package docker

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"trainshard/internal/domain/shared/vo"
)

func (c *Client) Sandbox(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (int, error) {
	name := sandboxName(shardID, node)

	found, present, err := c.inspectContainer(ctx, name)
	if err != nil {
		return 0, err
	}
	if !present {
		if err := c.createSandbox(ctx, name, shardID, node); err != nil {
			return 0, err
		}
	}
	if !present || !found.State.Running {
		if err := c.startByName(ctx, name); err != nil {
			return 0, err
		}
	}

	found, present, err = c.inspectContainer(ctx, name)
	if err != nil {
		return 0, err
	}
	if !present || found.State.Pid == 0 {
		return 0, fmt.Errorf("sandbox %s did not start", name)
	}
	return found.State.Pid, nil
}

func (c *Client) SandboxPID(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (int, bool, error) {
	found, present, err := c.inspectContainer(ctx, sandboxName(shardID, node))
	if err != nil || !present || !found.State.Running || found.State.Pid == 0 {
		return 0, false, err
	}
	return found.State.Pid, true, nil
}

func (c *Client) RemoveSandbox(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error {
	if err := c.removeByName(ctx, sandboxName(shardID, node)); err != nil {
		return err
	}
	c.log.Info("removed sandbox", "node_id", node.NodeID)
	return nil
}

func (c *Client) createSandbox(ctx context.Context, name string, shardID vo.ShardID, node vo.NodeRef) error {
	if err := c.pull(ctx, c.cfg.SandboxImage); err != nil {
		return err
	}

	ctx, cancel := c.bounded(ctx)
	defer cancel()

	_, err := c.engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   name,
		Config: &container.Config{Image: c.cfg.SandboxImage, Labels: labels(shardID, node, "sandbox")},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode("bridge"),
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		},
	})
	return err
}

func (c *Client) startByName(ctx context.Context, name string) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	_, err := c.engine.ContainerStart(ctx, name, client.ContainerStartOptions{})
	if settled(err) {
		return nil
	}
	return err
}
