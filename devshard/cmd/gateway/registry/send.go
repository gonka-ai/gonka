package registry

import (
	"context"
	"io"

	"devshard/gatewayproto"
	"devshard/host"
	"devshard/user"
)

// ErrSendPanicked marks a send whose read of the host's reply panicked. See ../docs/race.md.
var ErrSendPanicked = gatewayproto.ErrSendPanicked

type replySender interface {
	SendOnly(ctx context.Context, prepared *user.PreparedInference, stream io.Writer, onReceipt func()) (*host.HostResponse, error)
}

// SendRecovering is SendOnly with a panic while the host's reply is read turned into ErrSendPanicked. See ../docs/race.md.
func SendRecovering(ctx context.Context, session replySender, prepared *user.PreparedInference, stream io.Writer, onReceipt func()) (reply *host.HostResponse, err error) {
	return gatewayproto.SendRecovering(ctx, session, prepared, stream, onReceipt)
}
