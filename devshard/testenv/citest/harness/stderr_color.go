package harness

import "os"

// StderrColorEnabled reports whether stderr should receive ANSI styling (TTY and NO_COLOR unset).
// Kept in an untagged file so default tooling (gopls, go build ./...) loads package harness.
func StderrColorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
