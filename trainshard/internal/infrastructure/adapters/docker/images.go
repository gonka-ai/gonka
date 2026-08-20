package docker

import (
	"context"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func (c *Client) Has(ctx context.Context, digest vo.ImageDigest) (bool, error) {
	if err := pinned(digest); err != nil {
		return false, err
	}
	_, present, err := c.inspectImage(ctx, digest.String())
	return present, err
}

func (c *Client) Pull(ctx context.Context, digest vo.ImageDigest) error {
	if err := pinned(digest); err != nil {
		return err
	}
	return c.pull(ctx, digest.String())
}

// pull holds no timeout of its own: an image is gigabytes and the caller's context is the
// only sensible bound on how long that may take
func (c *Client) pull(ctx context.Context, reference string) error {
	response, err := c.engine.ImagePull(ctx, reference, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer response.Close()

	if err := response.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", reference, err)
	}
	return nil
}

func (c *Client) Layers(ctx context.Context, digest vo.ImageDigest) (vo.ImageLayers, error) {
	if err := pinned(digest); err != nil {
		return nil, err
	}
	image, present, err := c.inspectImage(ctx, digest.String())
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("image %s is not in the cache: %w", digest, shared.ErrNotFound)
	}
	return vo.ImageLayers(image.RootFS.Layers), nil
}

func (c *Client) inspectImage(ctx context.Context, reference string) (client.ImageInspectResult, bool, error) {
	ctx, cancel := c.bounded(ctx)
	defer cancel()

	image, err := c.engine.ImageInspect(ctx, reference)
	if cerrdefs.IsNotFound(err) {
		return client.ImageInspectResult{}, false, nil
	}
	if err != nil {
		return client.ImageInspectResult{}, false, err
	}
	return image, true, nil
}

// pinned refuses an image the proposal cannot have pinned: without a digest the same name can
// resolve to different bytes on two nodes of the same run
func pinned(digest vo.ImageDigest) error {
	if !strings.Contains(digest.String(), "@") {
		return fmt.Errorf("image %q must be named as repository@sha256:...: %w", digest, shared.ErrValidation)
	}
	return nil
}
