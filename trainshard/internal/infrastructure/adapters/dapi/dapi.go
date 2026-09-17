// Package dapi hands work to the participant's own dAPI: it holds the key this daemon does not,
// and it owns the ml node this daemon takes out of inference
package dapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Address     string
	Participant vo.Participant
	Timeout     time.Duration
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(client *http.Client, cfg Config) *Client {
	cfg.Address = strings.TrimSuffix(cfg.Address, "/")
	return &Client{cfg: cfg, http: client}
}

func (c *Client) call(ctx context.Context, method, path string, payload []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, method, c.cfg.Address+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return shared.New("DAPI_UNREACHABLE", shared.ErrUnavailable, err.Error())
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return shared.New("DAPI_REFUSED", shared.ErrUnavailable, fmt.Sprintf("the dapi answered %d to %s %s", response.StatusCode, method, path))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return shared.New("DAPI_ANSWER", shared.ErrUnavailable, fmt.Sprintf("%s %s: %v", method, path, err))
	}
	return nil
}
