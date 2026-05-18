//go:build dev || debug || development

package main

import (
	"log"
	"net/http"

	"devshard/internal/debugbuild"
)

func registerCtlDebugRoutes(mux *http.ServeMux, proxy *Proxy) {
	if !debugbuild.CtlDebugRoutesEnabled() {
		return
	}
	mux.HandleFunc("/v1/debug/cheat-anchor", proxy.handleDebugCheatAnchor)
	mux.HandleFunc("/v1/debug/arm-host-hold", proxy.handleDebugArmHostHold)
	log.Printf("devshardctl: debug routes enabled (/v1/debug/cheat-anchor, /v1/debug/arm-host-hold)")
}
