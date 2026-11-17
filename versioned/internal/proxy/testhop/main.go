// Command testhop serves versiond's proxy Handler for the devshard wrapping
// test. It is not a production binary: one version, HTTP/1.1 to the child.
package main

import (
	"log"
	"net/http"
	"os"
	"sync/atomic"

	"versioned/internal/proxy"
)

func main() {
	listen := os.Getenv("HOP_LISTEN")
	child := os.Getenv("HOP_CHILD")
	version := os.Getenv("HOP_VERSION")
	if listen == "" || child == "" || version == "" {
		log.Fatal("HOP_LISTEN, HOP_CHILD, and HOP_VERSION are required")
	}
	routes := &atomic.Value{}
	routes.Store(proxy.RouteTable{
		version: proxy.NewChildTarget(child, false),
	})
	srv := &http.Server{
		Addr:    listen,
		Handler: proxy.Handler(routes),
	}
	log.Fatal(srv.ListenAndServe())
}
