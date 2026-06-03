package apiconfig

import "fmt"

// MLNodeURL builds an MLnode URL, inserting the node version into the path
// when non-empty (the nginx version routing used for rolling upgrades).
//
// This is the single source of truth for MLnode URL construction so the
// broker, model manager, setup report, and pre-PoC node tester never drift
// from each other.
func MLNodeURL(host string, port int, segment, version string) string {
	if version == "" {
		return fmt.Sprintf("http://%s:%d%s", host, port, segment)
	}
	return fmt.Sprintf("http://%s:%d/%s%s", host, port, version, segment)
}
