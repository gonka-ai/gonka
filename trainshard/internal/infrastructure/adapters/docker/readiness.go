package docker

import (
	"context"
	"net/http"
	"net/url"
)

const readinessName = "trainshard-readiness"

func (c *Client) GPUContainer(ctx context.Context) error {
	if err := c.remove(ctx, readinessName); err != nil {
		return err
	}
	defer func() {
		if err := c.remove(context.WithoutCancel(ctx), readinessName); err != nil {
			c.log.Warn("readiness container is still on the machine", "error", err)
		}
	}()

	if err := c.createReadiness(ctx); err != nil {
		return err
	}

	_, err := c.call(ctx, http.MethodPost, "/containers/"+readinessName+"/start", nil, nil, nil)
	return err
}

func (c *Client) createReadiness(ctx context.Context) error {
	body := createRequest{
		Image: c.cfg.SandboxImage,
		Labels: map[string]string{
			labelRole: "readiness",
		},
		HostConfig: hostConfig{
			NetworkMode:    "none",
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			RestartPolicy:  restartPolicy{Name: "no"},
			DeviceRequests: gpuRequests(1),
		},
	}
	query := url.Values{"name": {readinessName}}
	status, err := c.call(ctx, http.MethodPost, "/containers/create", query, body, nil)
	if status != http.StatusNotFound {
		return err
	}
	if err := c.pullTag(ctx, c.cfg.SandboxImage); err != nil {
		return err
	}
	_, err = c.call(ctx, http.MethodPost, "/containers/create", query, body, nil)
	return err
}

func (c *Client) remove(ctx context.Context, name string) error {
	query := url.Values{"force": {"true"}, "v": {"false"}}

	status, err := c.call(ctx, http.MethodDelete, "/containers/"+name, query, nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}
