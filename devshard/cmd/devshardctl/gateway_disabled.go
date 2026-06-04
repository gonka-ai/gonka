package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type GatewayDisabledSettings struct {
	Enabled bool   `json:"enabled"`
	Message string `json:"message,omitempty"`
	NewURL  string `json:"new_url,omitempty"`
}

const defaultGatewayDisabledMessage = "please use ... base url"

func (s GatewayDisabledSettings) WithDefaults() GatewayDisabledSettings {
	s.Message = strings.TrimSpace(s.Message)
	if s.Message == "" {
		s.Message = defaultGatewayDisabledMessage
	}
	s.NewURL = strings.TrimSpace(s.NewURL)
	return s
}

func (g *Gateway) disabledMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		disabled := g.settings.Disabled
		g.mu.Unlock()
		disabled = disabled.WithDefaults()
		if !disabled.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		if isAdminPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		writeDisabledRedirect(w, disabled)
	})
}

func writeDisabledRedirect(w http.ResponseWriter, disabled GatewayDisabledSettings) {
	disabled = disabled.WithDefaults()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPermanentRedirect)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  http.StatusPermanentRedirect,
		"message": disabled.Message,
		"new_url": disabled.NewURL,
	})
}
