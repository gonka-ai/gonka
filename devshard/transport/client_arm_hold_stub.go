//go:build !dev && !debug && !development

package transport

import (
	"context"
	"fmt"
)

// ArmHoldInferenceResponse is unavailable unless built with -tags=dev, debug, or development.
func (c *HTTPClient) ArmHoldInferenceResponse(context.Context) error {
	return fmt.Errorf("inference hold debug not compiled in (rebuild with -tags=dev)")
}
