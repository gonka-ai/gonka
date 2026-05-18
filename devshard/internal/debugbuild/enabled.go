//go:build dev || debug || development

package debugbuild

// Enabled is true when the binary is built with -tags=dev, debug, or development.
const Enabled = true
