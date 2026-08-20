package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func (c *Client) Has(ctx context.Context, digest vo.ImageDigest) (bool, error) {
	_, present, err := c.inspectImage(ctx, digest)
	return present, err
}

func (c *Client) Pull(ctx context.Context, digest vo.ImageDigest) error {
	repository, hash, err := reference(digest)
	if err != nil {
		return err
	}

	return c.pull(ctx, repository, hash)
}

func (c *Client) pull(ctx context.Context, repository, tag string) error {
	body, err := c.stream(ctx, http.MethodPost, "/images/create", url.Values{
		"fromImage": {repository},
		"tag":       {tag},
	}, nil)
	if err != nil {
		return err
	}
	defer body.Close()

	lines := bufio.NewScanner(body)
	for lines.Scan() {
		var step struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(lines.Bytes(), &step); err != nil {
			continue
		}
		if step.Error != "" {
			return fmt.Errorf("pull %s:%s: %s", repository, tag, step.Error)
		}
	}
	return lines.Err()
}

func (c *Client) Layers(ctx context.Context, digest vo.ImageDigest) (vo.ImageLayers, error) {
	image, present, err := c.inspectImage(ctx, digest)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("image %s is not in the cache: %w", digest, shared.ErrNotFound)
	}
	return vo.ImageLayers(image.RootFS.Layers), nil
}

type imageInspect struct {
	RootFS struct {
		Layers []string `json:"Layers"`
	} `json:"RootFS"`
}

func (c *Client) inspectImage(ctx context.Context, digest vo.ImageDigest) (imageInspect, bool, error) {
	if _, _, err := reference(digest); err != nil {
		return imageInspect{}, false, err
	}

	var image imageInspect
	status, err := c.call(ctx, http.MethodGet, "/images/"+url.PathEscape(digest.String())+"/json", nil, nil, &image)
	if status == http.StatusNotFound {
		return imageInspect{}, false, nil
	}
	if err != nil {
		return imageInspect{}, false, err
	}
	return image, true, nil
}

func reference(digest vo.ImageDigest) (repository, hash string, err error) {
	repository, hash, found := strings.Cut(digest.String(), "@")
	if !found {
		return "", "", fmt.Errorf("image %q must be named as repository@sha256:...: %w", digest, shared.ErrValidation)
	}
	return repository, hash, nil
}
