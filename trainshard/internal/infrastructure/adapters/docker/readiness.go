package docker

import (
	"context"
	"errors"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"trainshard/internal/domain/shared"
)

// the name is fixed so that a box left behind by a daemon that died mid-check is cleared away by the
// next one rather than holding a card for good
const readinessName = "trainshard-readiness"

func (c *Client) GPUContainer(ctx context.Context) error {
	if err := c.removeByName(ctx, readinessName); err != nil {
		return unanswered(err)
	}
	defer func() {
		if err := c.removeByName(context.WithoutCancel(ctx), readinessName); err != nil {
			c.log.Warn("readiness container is still on the machine", "error", err)
		}
	}()

	if err := c.createReadiness(ctx); err != nil {
		return unanswered(err)
	}
	return unanswered(c.startByName(ctx, readinessName))
}

// unanswered marks an engine that failed to reach a verdict rather than reaching a bad one: it ran
// out of time, or it is still clearing away what the last check left behind. Neither says anything
// about the cards, and the caller holds a standing answer through them
func unanswered(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || cerrdefs.IsConflict(err) {
		return fmt.Errorf("%w: %w", shared.ErrUnavailable, err)
	}
	return err
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
