package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Version struct {
	Name   string `json:"name"`
	Binary string `json:"binary"`
	SHA256 string `json:"sha256,omitempty"`
	Port   int    `json:"port"`
}

type VersionConfig struct {
	Versions []Version `json:"versions"`
}

type store struct {
	mu       sync.RWMutex
	versions []Version
	binDir   string
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	binDir := os.Getenv("BINARY_DIR")
	if binDir == "" {
		binDir = "/data/binaries"
	}
	os.MkdirAll(binDir, 0755)

	s := &store{binDir: binDir}
	// Load initial config from file if exists
	if data, err := os.ReadFile("/data/versions.json"); err == nil {
		var cfg VersionConfig
		if err := json.Unmarshal(data, &cfg); err == nil {
			s.versions = cfg.Versions
			log.Printf("loaded %d initial versions", len(s.versions))
		}
	}

	http.HandleFunc("/versions", func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(VersionConfig{Versions: s.versions})
	})

	http.HandleFunc("/versions/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/versions/")
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			var v Version
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			v.Name = name
			s.mu.Lock()
			found := false
			for i, existing := range s.versions {
				if existing.Name == name {
					s.versions[i] = v
					found = true
					break
				}
			}
			if !found {
				s.versions = append(s.versions, v)
			}
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(v)
		case http.MethodDelete:
			s.mu.Lock()
			for i, v := range s.versions {
				if v.Name == name {
					s.versions = append(s.versions[:i], s.versions[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/binaries/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/binaries/")
		if strings.Contains(name, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		path := filepath.Join(s.binDir, name)
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := os.WriteFile(path, data, 0644); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.ServeFile(w, r, path)
		}
	})

	addr := fmt.Sprintf(":%s", port)
	log.Printf("mock oracle listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
