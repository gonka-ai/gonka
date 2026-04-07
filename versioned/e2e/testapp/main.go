package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := flag.Int("port", 8080, "listen port")
	dataDir := flag.String("data-dir", "", "data directory")
	flag.Parse()

	prefix := os.Getenv("SUBNET_LOG_PREFIX")
	log.Printf("[%s] starting testapp on port %d, data-dir=%s", prefix, *port, *dataDir)

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

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[%s] listening on %s", prefix, addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal(err)
	}
}
