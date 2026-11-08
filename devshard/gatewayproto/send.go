package gatewayproto

import (
	"context"
	"errors"
	"fmt"
	"io"

	"devshard/host"
	"devshard/user"
)

// ErrSendPanicked marks a send whose read of the host's reply panicked.
var ErrSendPanicked = errors.New("send panicked")

type ReplySender interface {
	SendOnly(ctx context.Context, prepared *user.PreparedInference, stream io.Writer, onReceipt func()) (*host.HostResponse, error)
}

// SendRecovering turns a panic while reading a host reply into ErrSendPanicked.
func SendRecovering(ctx context.Context, session ReplySender, prepared *user.PreparedInference, stream io.Writer, onReceipt func()) (reply *host.HostResponse, err error) {
	defer func() {
		if panicked := recover(); panicked != nil {
			reply, err = nil, fmt.Errorf("%w: %v", ErrSendPanicked, panicked)
		}
	}()
	return session.SendOnly(ctx, prepared, stream, onReceipt)
}
