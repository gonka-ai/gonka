package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	pb "versioned/e2e/testapp/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Version is the protocol slot name for e2e (maps to approved_versions.name).
// versiond requires --print-protocol-version to equal the oracle slot name, so
// e2e registers the slot as "testapp". A second binary can override via
// -ldflags "-X main.Version=testapp2 -X main.BinaryVersion=testapp2".
var Version = "testapp"

// BinaryVersion is the build id printed by --print-binary-version and exposed
// as DEVSHARD_BINARY_LOG_VERSION (used as the HTTP "prefix" field).
var BinaryVersion = "testapp"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--print-binary-version" {
		fmt.Println(BinaryVersion)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--print-protocol-version" {
		fmt.Println(Version)
		return
	}

	port := flag.Int("port", 8080, "listen port")
	dataDir := flag.String("data-dir", "", "data directory")
	flag.Parse()

	prefix := os.Getenv("DEVSHARD_BINARY_LOG_VERSION")
	if prefix == "" {
		prefix = BinaryVersion
	}
	nmAddr := os.Getenv("NODE_MANAGER_ADDR")
	log.Printf("[%s] starting testapp on port %d, data-dir=%s, node-manager=%s", prefix, *port, *dataDir, nmAddr)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"version": "testapp",
			"prefix":  prefix,
		})
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: event %d\n\n", i)
			flusher.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	})

	http.HandleFunc("/nodemanager-test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if nmAddr == "" {
			json.NewEncoder(w).Encode(map[string]string{
				"error": "NODE_MANAGER_ADDR not set",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		conn, err := grpc.NewClient(nmAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			json.NewEncoder(w).Encode(map[string]string{
				"error":             fmt.Sprintf("grpc dial failed: %v", err),
				"nodemanager_addr":  nmAddr,
			})
			return
		}
		defer conn.Close()

		client := pb.NewNodeManagerClient(conn)

		acquireResp, err := client.AcquireMLNode(ctx, &pb.AcquireMLNodeRequest{
			Model: "test-model",
		})
		if err != nil {
			json.NewEncoder(w).Encode(map[string]string{
				"error":            fmt.Sprintf("acquire failed: %v", err),
				"grpc_connected":   "true",
				"nodemanager_addr": nmAddr,
			})
			return
		}

		_, releaseErr := client.ReleaseMLNode(ctx, &pb.ReleaseMLNodeRequest{
			LockId:  acquireResp.LockId,
			Outcome: pb.ReleaseOutcome_SUCCESS,
		})

		result := map[string]string{
			"endpoint":         acquireResp.Endpoint,
			"node_id":          acquireResp.NodeId,
			"lock_id":          acquireResp.LockId,
			"nodemanager_addr": nmAddr,
			"grpc_connected":   "true",
		}
		if releaseErr != nil {
			result["release_error"] = releaseErr.Error()
		}
		json.NewEncoder(w).Encode(result)
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[%s] listening on %s", prefix, addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
