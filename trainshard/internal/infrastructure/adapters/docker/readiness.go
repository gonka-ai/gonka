package docker

import (
	"context"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const readinessName = "trainshard-readiness"

func (c *Client) GPUContainer(ctx context.Context) error {
	if err := c.removeByName(ctx, readinessName); err != nil {
		return err
	}
	defer func() {
		if err := c.removeByName(context.WithoutCancel(ctx), readinessName); err != nil {
			c.log.Warn("readiness container is still on the machine", "error", err)
		}
	}()

	if err := c.createReadiness(ctx); err != nil {
		return err
	}
	return c.startByName(ctx, readinessName)
}

func (c *Client) createReadiness(ctx context.Context) error {
	options := client.ContainerCreateOptions{
		Name:   readinessName,
		Config: &container.Config{Image: c.cfg.SandboxImage, Labels: map[string]string{labelRole: "readiness"}},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode("none"),
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges"},
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Resources:     container.Resources{DeviceRequests: c.gpuRequests(1)},
		},
	}

	if err := c.create(ctx, options); !cerrdefs.IsNotFound(err) {
		return err
	}
	if err := c.pull(ctx, c.cfg.SandboxImage); err != nil {
		return err
	}
	return c.create(ctx, options)
}

func (c *Client) create(ctx context.Context, options client.ContainerCreateOptions) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	_, err := c.engine.ContainerCreate(ctx, options)
	return err
}
