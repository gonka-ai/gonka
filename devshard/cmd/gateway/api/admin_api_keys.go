package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/store"
)

const (
	generatedAPIKeyPrefix = "sk-"
	generatedAPIKeyBytes  = 24
	keyNotSupplied        = ""
)

type createAPIKeyRequest struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type apiKeyView struct {
	ID        string       `json:"id"`
	Source    apiKeySource `json:"source"`
	Label     string       `json:"label"`
	CreatedAt string       `json:"created_at,omitempty"`
	Revoked   bool         `json:"revoked"`
	RevokedAt string       `json:"revoked_at,omitempty"`
}

type apiKeyWithValue struct {
	apiKeyView
	Key string `json:"key"`
}

func viewOfAPIKey(record store.APIKeyRecord) apiKeyView {
	return apiKeyView{
		ID:        record.ID,
		Source:    apiKeySourceDatabase,
		Label:     record.Label,
		CreatedAt: store.FormatTime(record.CreatedAt),
		Revoked:   record.Revoked(),
		RevokedAt: store.FormatTime(record.RevokedAt),
	}
}

func viewOfEnvAPIKey(id string) apiKeyView {
	return apiKeyView{ID: id, Source: apiKeySourceEnv}
}

func viewOfAPIKeyWithValue(record store.APIKeyRecord) apiKeyWithValue {
	return apiKeyWithValue{apiKeyView: viewOfAPIKey(record), Key: record.Key}
}

func (s *Server) handleAdminAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodPost) {
		return
	}
	if r.Method == http.MethodPost {
		s.createAPIKey(w, r)
		return
	}
	records, err := s.control.ListAPIKeys(r.Context())
	if writeControlFailure(w, err) {
		return
	}
	views := make([]apiKeyView, 0, len(records))
	for _, record := range records {
		views = append(views, viewOfAPIKey(record))
	}
	for _, key := range s.config.Load().Server.EnvAPIKeys {
		views = append(views, viewOfEnvAPIKey(idOfAPIKey(key)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": views})
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var request createAPIKeyRequest
	if err := decodeAdminBodyStrictly(w, r, &request); err != nil {
		s.writeErrorFor(w, badRequestUnlessOversized(err))
		return
	}
	key := request.Key
	if key == keyNotSupplied {
		generated, err := generateAPIKey()
		if writeControlFailure(w, err) {
			return
		}
		key = generated
	}
	record := store.APIKeyRecord{ID: idOfAPIKey(key), Key: key, Label: strings.TrimSpace(request.Label), CreatedAt: s.now()}
	if s.isEnvAPIKey(record.ID) {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrEnvAPIKey, record.ID))
		return
	}
	s.settingsWrites.Lock()
	defer s.settingsWrites.Unlock()
	if err := s.operations.CreateAPIKey(r.Context(), record); err != nil {
		s.writeErrorFor(w, err)
		return
	}
	auditAdmin("api key created", logkey.APIKeyID, record.ID, logkey.APIKeyLabel, record.Label)
	writeJSON(w, http.StatusOK, viewOfAPIKeyWithValue(record))
}

func (s *Server) handleAdminAPIKey(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodDelete) {
		return
	}
	if r.Method == http.MethodDelete {
		s.revokeAPIKey(w, r)
		return
	}
	if id := r.PathValue("id"); s.isEnvAPIKey(id) {
		writeJSON(w, http.StatusOK, viewOfEnvAPIKey(id))
		return
	}
	record, found := s.storedAPIKey(w, r)
	if !found {
		return
	}
	auditAdmin("api key read", logkey.APIKeyID, record.ID, logkey.APIKeyLabel, record.Label)
	writeJSON(w, http.StatusOK, viewOfAPIKeyWithValue(record))
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	s.settingsWrites.Lock()
	defer s.settingsWrites.Unlock()
	if id := r.PathValue("id"); s.isEnvAPIKey(id) {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrEnvAPIKey, id))
		return
	}
	record, found := s.storedAPIKey(w, r)
	if !found {
		return
	}
	if !record.Revoked() {
		revokedAt := s.now()
		if err := s.operations.RevokeAPIKey(r.Context(), record.ID, revokedAt); err != nil {
			s.writeErrorFor(w, err)
			return
		}
		record.RevokedAt = revokedAt
		auditAdmin("api key revoked", logkey.APIKeyID, record.ID, logkey.APIKeyLabel, record.Label)
	}
	writeJSON(w, http.StatusOK, viewOfAPIKey(record))
}

func (s *Server) isEnvAPIKey(id string) bool {
	return slices.ContainsFunc(s.config.Load().Server.EnvAPIKeys, func(key string) bool { return idOfAPIKey(key) == id })
}

func (s *Server) storedAPIKey(w http.ResponseWriter, r *http.Request) (store.APIKeyRecord, bool) {
	id := r.PathValue("id")
	record, found, err := s.control.APIKey(r.Context(), id)
	if writeControlFailure(w, err) {
		return store.APIKeyRecord{}, false
	}
	if !found {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownAPIKey, id))
		return store.APIKeyRecord{}, false
	}
	return record, true
}

func generateAPIKey() (string, error) {
	random := make([]byte, generatedAPIKeyBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generating an API key: %w", err)
	}
	return generatedAPIKeyPrefix + hex.EncodeToString(random), nil
}
