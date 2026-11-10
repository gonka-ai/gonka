package api

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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

// The body limits are in README.md, "Reading a body"; the client write bound in "Streaming the reply"; hostCatchUpReserveBytes is in request.md; apiKeyIDBytes is in README.md, "Authentication and the kill switch".
const (
	chatIngestLimit = filters.MaxBodyBytes

	hostCatchUpReserveBytes = 1 << 20
	adminIngestLimit        = 64 << 10
	bodyReadTimeout         = 30 * time.Second
	clientWriteTimeout      = bodyReadTimeout
	clientWriteChunkBytes   = 64 << 10

	bodyReadStart = 16 << 10

	apiKeyIDBytes = 4
)

// credentials is one request's resolved identity, computed only where an answer is used.
type credentials struct {
	admin    bool
	apiKey   bool
	apiKeyID string
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

func (g keyGate) authenticate(authorization string) bool {
	_, matched := g.match(authorization)
	return matched
}

// match compares every configured key, so the time taken does not depend on which one matched.
func (g keyGate) match(authorization string) ([sha256.Size]byte, bool) {
	presented, hasBearer := bearerKey(authorization)
	if !g.configured || !hasBearer {
		return [sha256.Size]byte{}, false
	}
	digest := sha256.Sum256([]byte(presented))
	matched := 0
	for _, configured := range g.digests {
		matched |= subtle.ConstantTimeCompare(digest[:], configured[:])
	}
	return digest, matched == 1
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
	clientDigest, apiKey := gates.client.match(authorization)
	identity := credentials{admin: gates.admin.authenticate(authorization), apiKey: apiKey}
	if apiKey {
		identity.apiKeyID = hex.EncodeToString(clientDigest[:apiKeyIDBytes])
	}
	return identity
}

func (c credentials) caller() string {
	switch {
	case c.apiKey:
		return c.apiKeyID
	case c.admin:
		return callerAdmin
	}
	return callerAnonymous
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
func readBody(w http.ResponseWriter, r *http.Request, limit int64, budget *BufferBudget) ([]byte, error) {
	deadlines := http.NewResponseController(w)
	_ = deadlines.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	r.Body = http.MaxBytesReader(baseWriter(w), r.Body, limit)
	body, err := readAll(r.Body, budget)
	_ = deadlines.SetReadDeadline(time.Time{})
	return body, bodyReadFailure(w, err)
}

// readAll is io.ReadAll starting wide enough for an ordinary body. The start is a constant, never the declared Content-Length: a header the client has not backed with bytes must buy no memory. See README.md, "Reading a body".
func readAll(reader io.Reader, budget *BufferBudget) ([]byte, error) {
	body := make([]byte, 0, bodyReadStart)
	for {
		if len(body) == cap(body) {
			body = append(body, 0)[:len(body)]
		}
		read, err := reader.Read(body[len(body):cap(body)])
		if !budget.reserve(int64(read)) {
			budget.release(int64(len(body)))
			return nil, ErrRequestBodiesFull
		}
		body = body[:len(body)+read]
		if err != nil {
			if errors.Is(err, io.EOF) {
				return body, nil
			}
			budget.release(int64(len(body)))
			return body, err
		}
	}
}

func (s *Server) boundClientWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bounded := newDeadlineWriter(w, s.config.Load().Limits.MinClientReadBytesPerSecond, time.Now)
		bounded.extendDeadline()
		next.ServeHTTP(bounded, r)
	})
}

type deadlineWriter struct {
	http.ResponseWriter
	controller  *http.ResponseController
	minReadRate int64
	now         func() time.Time
	delivered   int64
	blocked     time.Duration
}

func newDeadlineWriter(w http.ResponseWriter, minReadRate int64, now func() time.Time) *deadlineWriter {
	return &deadlineWriter{ResponseWriter: w, controller: http.NewResponseController(w), minReadRate: minReadRate, now: now}
}

func (w *deadlineWriter) Write(data []byte) (int, error) {
	written := 0
	for {
		end := min(len(data), clientWriteChunkBytes)
		started := w.extendDeadline()
		sent, err := w.ResponseWriter.Write(data[:end])
		w.charge(started, sent)
		written += sent
		data = data[end:]
		if err != nil || len(data) == 0 {
			return written, err
		}
	}
}

func (w *deadlineWriter) Flush() { _ = w.FlushError() }

func (w *deadlineWriter) FlushError() error {
	started := w.extendDeadline()
	err := w.controller.Flush()
	w.charge(started, 0)
	return err
}

func (w *deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *deadlineWriter) extendDeadline() time.Time {
	now := w.now()
	_ = w.controller.SetWriteDeadline(now.Add(min(clientWriteTimeout, w.allowance())))
	return now
}

func (w *deadlineWriter) allowance() time.Duration {
	earned := time.Duration(float64(w.delivered) / float64(w.minReadRate) * float64(time.Second))
	return clientWriteTimeout + earned - w.blocked
}

func (w *deadlineWriter) charge(started time.Time, sent int) {
	w.blocked += w.now().Sub(started)
	w.delivered += int64(sent)
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
	body, err := readBody(w, r, adminIngestLimit, nil)
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
