package user

import "devshard/transport"

var (
	_ HostClient        = (*transport.HTTPClient)(nil)
	_ HostClient        = (*transport.RPCClient)(nil)
	_ SignatureFetcher = (*transport.HTTPClient)(nil)
	_ SignatureFetcher = (*transport.RPCClient)(nil)
)
