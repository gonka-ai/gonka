package api

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/internal/strictjson"
)

// The body limits are in README.md, "Reading a body"; hostCatchUpReserveBytes is in request.md.
const (
	chatIngestLimit = filters.MaxBodyBytes

	hostCatchUpReserveBytes = 1 << 20
	adminIngestLimit        = 64 << 10
	bodyReadTimeout         = 30 * time.Second

	bodyReadStart = 16 << 10
)

// credentials is one request's resolved identity, computed only where an answer is used.
type credentials struct {
	admin  bool
	apiKey bool
}

// keyGate compares a bearer against configured keys. See README.md, "Authentication and the kill switch".
type keyGate struct {
	digests    [][sha256.Size]byte
	configured bool
}

// keyGates are one configuration snapshot's gates, hashed once for as long as that snapshot is live.
type keyGates struct {
	source *config.Config
	admin  keyGate
	client keyGate
}

func newKeyGate(keys ...string) keyGate {
	gate := keyGate{}
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			continue
		}
		gate.digests = append(gate.digests, sha256.Sum256([]byte(trimmed)))
		gate.configured = true
	}
	return gate
}

func bearerKey(authorization string) (string, bool) {
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

// authenticate compares every configured key, so the time taken does not depend on which one matched.
func (g keyGate) authenticate(authorization string) bool {
	presented, hasBearer := bearerKey(authorization)
	if !g.configured || !hasBearer {
		return false
	}
	digest := sha256.Sum256([]byte(presented))
	matched := 0
	for _, configured := range g.digests {
		matched |= subtle.ConstantTimeCompare(digest[:], configured[:])
	}
	return matched == 1
}

// requireAdmin gates the operator routes. See README.md, "Authentication and the kill switch".
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminEnabled() {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		if !s.authenticateAdmin(r) {
			writeJSON(w, http.StatusUnauthorized, errorEnvelope{Error: errorDetail{
				Message: "Invalid admin API key.",
				Type:    "invalid_request_error",
				Code:    "invalid_api_key",
			}})
			return
		}
		next(w, r)
	}
}

func (s *Server) adminEnabled() bool {
	return s.config.Load().Server.AdminEnabled()
}

func (s *Server) authenticateAdmin(r *http.Request) bool {
	return s.resolveCredentials(r).admin
}

// resolveCredentials answers only when a credential was presented, so an unauthenticated request compares nothing.
func (s *Server) resolveCredentials(r *http.Request) credentials {
	authorization := r.Header.Get("Authorization")
	if authorization == "" {
		return credentials{}
	}
	return s.verify(authorization)
}

func (s *Server) compareKeys(authorization string) credentials {
	gates := s.keyGates()
	return credentials{
		admin:  gates.admin.authenticate(authorization),
		apiKey: gates.client.authenticate(authorization),
	}
}

// keyGates hashes the configured keys once per snapshot: every request compares, none re-digests.
func (s *Server) keyGates() *keyGates {
	configuration := s.config.Load()
	if cached := s.gates.Load(); cached != nil && cached.source == configuration {
		return cached
	}
	server := configuration.Server
	built := &keyGates{source: configuration, client: newKeyGate(server.APIKeys...)}
	if server.AdminEnabled() {
		built.admin = newKeyGate(server.AdminAPIKey)
	}
	s.gates.Store(built)
	return built
}

// disabled is the operator kill switch; alwaysOn routes stay reachable. See operations.md, "The kill switch".
func (s *Server) disabled(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modes := s.config.Load().Modes
		if !modes.Disabled {
			next(w, r)
			return
		}
		if redirect := strings.TrimSpace(modes.DisabledRedirectURL); redirect != "" {
			w.Header().Set("Location", redirect)
			writeJSON(w, http.StatusPermanentRedirect, map[string]any{
				"status":  http.StatusPermanentRedirect,
				"message": modes.DisabledMessage,
				"new_url": redirect,
			})
			return
		}
		message := strings.TrimSpace(modes.DisabledMessage)
		if message == "" {
			message = "gateway is disabled"
		}
		writeError(w, http.StatusServiceUnavailable, message)
	}
}

// readBody bounds ingest with MaxBytesReader, not LimitReader. See README.md, "Reading a body".
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	deadlines := http.NewResponseController(w)
	_ = deadlines.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	r.Body = http.MaxBytesReader(baseWriter(w), r.Body, limit)
	body, err := readAll(r.Body)
	_ = deadlines.SetReadDeadline(time.Time{})
	return body, err
}

// readAll is io.ReadAll starting wide enough for an ordinary body. The start is a constant, never the declared Content-Length: a header the client has not backed with bytes must buy no memory. See README.md, "Reading a body".
func readAll(reader io.Reader) ([]byte, error) {
	body := make([]byte, 0, bodyReadStart)
	for {
		if len(body) == cap(body) {
			body = append(body, 0)[:len(body)]
		}
		read, err := reader.Read(body[len(body):cap(body)])
		body = body[:len(body)+read]
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return body, err
		}
	}
}

// baseWriter walks the Unwrap chain: MaxBytesReader marks a connection by type-asserting the writer it is handed.
func baseWriter(w http.ResponseWriter) http.ResponseWriter {
	for {
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = unwrapper.Unwrap()
	}
}

func decodeAdminBody(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeAdminBodyWith(w, r, target, strictjson.Unmarshal)
}

func decodeAdminBodyStrictly(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeAdminBodyWith(w, r, target, strictjson.DecodeKnownFields)
}

func decodeAdminBodyWith(w http.ResponseWriter, r *http.Request, target any, decode func([]byte, any) error) error {
	body, err := readBody(w, r, adminIngestLimit)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	return decode(body, target)
}

func allowMethods(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	if slices.Contains(methods, r.Method) {
		return true
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}
