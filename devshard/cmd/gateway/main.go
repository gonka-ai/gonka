// Command gateway is the devshard gateway between the broker and race participants. See README.md.
package main

import "devshard/cmd/gateway/app"

// Version is stamped by the build via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	app.Version = Version
	app.Main()
}
