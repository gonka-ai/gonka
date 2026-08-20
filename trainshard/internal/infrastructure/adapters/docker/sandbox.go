package docker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	if !found.State.Running {
		if _, err := c.call(ctx, http.MethodPost, "/containers/"+name+"/start", nil, nil, nil); err != nil {
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
	if err := c.remove(ctx, sandboxName(shardID, node)); err != nil {
		return err
	}
	c.log.Info("removed sandbox", "node_id", node.NodeID)
	return nil
}

func (c *Client) createSandbox(ctx context.Context, name string, shardID vo.ShardID, node vo.NodeRef) error {
	if err := c.pullTag(ctx, c.cfg.SandboxImage); err != nil {
		return err
	}

	body := createRequest{
		Image:  c.cfg.SandboxImage,
		Labels: labels(shardID, node, "sandbox"),
		HostConfig: hostConfig{
			NetworkMode:   "bridge",
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges"},
			RestartPolicy: restartPolicy{Name: "no"},
		},
	}

	_, err := c.call(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, body, nil)
	return err
}

func (c *Client) pullTag(ctx context.Context, image string) error {
	repository, tag := image, "latest"
	if at := strings.LastIndex(image, ":"); at > strings.LastIndex(image, "/") {
		repository, tag = image[:at], image[at+1:]
	}
	return c.pull(ctx, repository, tag)
}
