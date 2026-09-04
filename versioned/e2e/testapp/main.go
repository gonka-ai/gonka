package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "versioned/e2e/testapp/gen"
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
	var ready atomic.Bool
	var draining atomic.Bool
	var inflight atomic.Int64
	ready.Store(true)
	log.Printf("[%s] starting testapp on port %d, data-dir=%s, node-manager=%s", prefix, *port, *dataDir, nmAddr)

	track := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if draining.Load() {
				http.Error(w, "draining", http.StatusServiceUnavailable)
				return
			}
			inflight.Add(1)
			defer inflight.Add(-1)
			fn(w, r)
		}
	}

	writeVersion := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{
			"version": "testapp",
			"prefix":  prefix,
		}); err != nil {
			log.Printf("write version response: %v", err)
		}
	}

	http.HandleFunc("/", track(func(w http.ResponseWriter, r *http.Request) {
		writeVersion(w)
	}))

	http.HandleFunc("/slow", track(func(w http.ResponseWriter, r *http.Request) {
		delay := 8 * time.Second
		if raw := r.URL.Query().Get("duration"); raw != "" {
			if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
				delay = parsed
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
		writeVersion(w)
	}))

	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		status := map[string]any{
			"ready":    ready.Load(),
			"draining": draining.Load(),
			"inflight": inflight.Load(),
		}
		w.Header().Set("Content-Type", "application/json")
		if !ready.Load() || draining.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(status)
	})

	http.HandleFunc("/drain", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		draining.Store(true)
		ready.Store(false)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ready":    ready.Load(),
			"draining": draining.Load(),
			"inflight": inflight.Load(),
		})
	})

	http.HandleFunc("/drain/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ready":    ready.Load(),
			"draining": draining.Load(),
			"inflight": inflight.Load(),
		})
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)

		go func() {
			time.Sleep(50 * time.Millisecond)
			os.Exit(42)
		}()
	})

	http.HandleFunc("/stream", track(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		for i := range 5 {
			_, _ = fmt.Fprintf(w, "data: event %d\n\n", i)
			flusher.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))

	http.HandleFunc("/nodemanager-test", track(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if nmAddr == "" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "NODE_MANAGER_ADDR not set",
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		conn, err := grpc.NewClient(nmAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":            fmt.Sprintf("grpc dial failed: %v", err),
				"nodemanager_addr": nmAddr,
			})
			return
		}
		defer func() { _ = conn.Close() }()

		client := pb.NewNodeManagerClient(conn)

		acquireResp, err := client.AcquireMLNode(ctx, &pb.AcquireMLNodeRequest{
			Model: "test-model",
		})
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":            fmt.Sprintf("acquire failed: %v", err),
				"grpc_connected":   "true",
				"nodemanager_addr": nmAddr,
			})
			return
		}

		_, releaseErr := client.ReleaseMLNode(ctx, &pb.ReleaseMLNodeRequest{
			LockId:  acquireResp.GetLockId(),
			Outcome: pb.ReleaseOutcome_SUCCESS,
		})

		result := map[string]string{
			"endpoint":         acquireResp.GetEndpoint(),
			"node_id":          acquireResp.GetNodeId(),
			"lock_id":          acquireResp.GetLockId(),
			"nodemanager_addr": nmAddr,
			"grpc_connected":   "true",
		}
		if releaseErr != nil {
			result["release_error"] = releaseErr.Error()
		}
		_ = json.NewEncoder(w).Encode(result)
	}))

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[%s] listening on %s", prefix, addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
