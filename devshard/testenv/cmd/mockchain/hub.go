package main

import (
	"context"
	"errors"
	"net/http"
	"time"
)

func serveHub(ctx context.Context, addr, dir, certFile, keyFile string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           http.FileServer(http.Dir(dir)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.ListenAndServeTLS(certFile, keyFile); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
