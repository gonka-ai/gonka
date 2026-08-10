//go:build !testenvci

package host

import "net/http"

// injectPrimaryDetachFault is the production no-op. The mid-stream detach fault
// used by the reconnect citest lives in livestream_fault_testenvci.go and is
// only compiled with -tags testenvci, so no release binary can be made to tear
// down a client connection through an environment variable.
func (s *LiveStream) injectPrimaryDetachFault(http.ResponseWriter, *liveSubscriber) bool {
	return false
}
