//go:build dev || debug || development

package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"devshard/internal/debugbuild"
)

// ArmHoldInferenceResponse arms the testenv debug hook that blocks the next inference
// HTTP response before SSE. Caller must be built with -tags=dev; the target host must have
// registered /v1/debug/arm-hold-inference-response (DEVSHARDD_DEBUG=1 in that container).
func (c *HTTPClient) ArmHoldInferenceResponse(ctx context.Context) error {
	if !debugbuild.Enabled {
		return fmt.Errorf("inference hold debug not compiled in (rebuild with -tags=dev)")
	}
	url := c.baseURL + "/v1/debug/arm-hold-inference-response"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("arm hold inference response: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("arm hold inference response: status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}
	return nil
}
