package debugbuild

import "os"

// HostDebugRoutesEnabled reports whether devshardd should register inference-hold debug HTTP routes.
func HostDebugRoutesEnabled() bool {
	return Enabled && os.Getenv("DEVSHARDD_DEBUG") == "1"
}

// CtlDebugRoutesEnabled reports whether devshardctl should register testenv debug proxy routes.
func CtlDebugRoutesEnabled() bool {
	return Enabled && os.Getenv("DEVSHARDCTL_DEBUG") == "1"
}
