package main

import (
	"log"
	"os"
	"strings"
	"sync"
)

// envGatewayCompressRequestBodies used to gzip HTTP request bodies. Phase 7
// retired that path; a set variable is ignored.
const envGatewayCompressRequestBodies = "DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES"

var noteRetiredCompressOnce sync.Once

func noteRetiredCompressRequestBodies() {
	if strings.TrimSpace(os.Getenv(envGatewayCompressRequestBodies)) == "" {
		return
	}
	noteRetiredCompressOnce.Do(func() {
		log.Printf("%s is retired; peer HTTP request bodies are not gzipped", envGatewayCompressRequestBodies)
	})
}
