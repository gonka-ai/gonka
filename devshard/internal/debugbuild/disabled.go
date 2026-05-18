//go:build !dev && !debug && !development

package debugbuild

// Enabled is false unless the binary is built with -tags=dev, debug, or development.
const Enabled = false
