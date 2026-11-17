package api

import (
	"cmp"
	"fmt"
	"net/http"
	"net/http/pprof"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/scheduler"
	"devshard/observability"
	"devshard/transport"
	"devshard/user"
)

// otherRouteLabel is the single label every unmatched path folds into. See operations.md, "Cardinality rules".
const otherRouteLabel = "other"

// route is one registered pattern; an empty label leaves it uninstrumented. See README.md, "The route table".
type route struct {
	pattern  string
	label    string
	admin    bool
	alwaysOn bool
	handler  http.HandlerFunc
}

func (s *Server) routes() []route {
	return []route{
		{pattern: "/metrics", alwaysOn: true, handler: s.handleMetrics},
		{pattern: "/healthz", alwaysOn: true, handler: s.handleHealthz},
		{pattern: "/v1/models", label: "/v1/models", handler: s.handleModels},
		{pattern: "/v1/chat/completions", label: "/v1/chat/completions", handler: s.handleChat},
		{pattern: "/v1/status", label: "/v1/status", alwaysOn: true, handler: s.handleStatus},
		// Admin-only: the row has no caller to authorise against. See operations.md, "What is exposed".
		{pattern: "/v1/requests/{id}", label: "/v1/requests/{id}", admin: true, alwaysOn: true, handler: s.handleRequestAccounting},

		{pattern: "/devshard/{id}/v1/models", label: "/devshard/{id}/v1/models", handler: s.handleDevshardModels},
		{pattern: "/devshard/{id}/v1/chat/completions", label: "/devshard/{id}/v1/chat/completions", handler: s.handleDevshardChat},
		{pattern: "/devshard/{id}/v1/status", label: "/devshard/{id}/v1/status", handler: s.handleDevshardStatus},
		{pattern: "/devshard/{id}/v1/finalize", label: "/devshard/{id}/v1/finalize", admin: true, alwaysOn: true, handler: s.handleDevshardFinalize},

		// Recovery surface: admin and alwaysOn. See operations.md, "What is exposed".
		{pattern: "/devshard/{id}/v1/state", label: "/devshard/{id}/v1/state", admin: true, alwaysOn: true, handler: s.handleDevshardState},
		{pattern: "/devshard/{id}/v1/debug/state", label: "/devshard/{id}/v1/debug/state", admin: true, alwaysOn: true, handler: s.handleDevshardDebugState},
		{pattern: "/devshard/{id}/v1/debug/inferences", label: "/devshard/{id}/v1/debug/inferences", admin: true, alwaysOn: true, handler: s.handleDevshardDebugInferences},
		{pattern: "/devshard/{id}/v1/debug/pending", label: "/devshard/{id}/v1/debug/pending", admin: true, alwaysOn: true, handler: s.handleDevshardDebugPending},
		{pattern: "/devshard/{id}/v1/debug/signatures", label: "/devshard/{id}/v1/debug/signatures", admin: true, alwaysOn: true, handler: s.handleDevshardDebugSignatures},
		{pattern: "/devshard/{id}/v1/debug/signatures/collect", label: "/devshard/{id}/v1/debug/signatures/collect", admin: true, alwaysOn: true, handler: s.handleDevshardCollectSignatures},

		{pattern: "/v1/admin/state", label: "/v1/admin/state", admin: true, alwaysOn: true, handler: s.handleAdminState},
		{pattern: "/v1/admin/settings", label: "/v1/admin/settings", admin: true, alwaysOn: true, handler: s.handleAdminSettings},
		{pattern: "/v1/admin/devshards", label: "/v1/admin/devshards", admin: true, alwaysOn: true, handler: s.handleAdminDevshards},
		// The templated label is deliberate: a label of its own would split the panel that reads the series.
		{pattern: "/v1/admin/devshards/import", label: "/v1/admin/devshards/{id}", admin: true, alwaysOn: true, handler: s.handleAdminDevshardImport},
		// A literal beats the {id} template, and escrow ids are numeric, so this shadows no escrow.
		{pattern: "/v1/admin/devshards/settle", label: "/v1/admin/devshards/settle", admin: true, alwaysOn: true, handler: s.handleAdminDevshardsSettleBatch},
		{pattern: "/v1/admin/devshards/deactivate", label: "/v1/admin/devshards/deactivate", admin: true, alwaysOn: true, handler: s.handleAdminDevshardsDeactivateBatch},
		{pattern: "/v1/admin/devshards/{id}", label: "/v1/admin/devshards/{id}", admin: true, alwaysOn: true, handler: s.handleAdminDevshardDelete},
		{pattern: "/v1/admin/devshards/{id}/activate", label: "/v1/admin/devshards/{id}/activate", admin: true, alwaysOn: true, handler: s.handleAdminDevshardActivate},
		{pattern: "/v1/admin/devshards/{id}/deactivate", label: "/v1/admin/devshards/{id}/deactivate", admin: true, alwaysOn: true, handler: s.handleAdminDevshardDeactivate},
		{pattern: "/v1/admin/devshards/{id}/settle", label: "/v1/admin/devshards/{id}/settle", admin: true, alwaysOn: true, handler: s.handleAdminDevshardSettle},
		{pattern: "/v1/admin/devshards/{id}/participants", label: "/v1/admin/devshards/{id}/participants", admin: true, alwaysOn: true, handler: s.handleAdminDevshardParticipants},
		{pattern: "/v1/admin/escrows", label: "/v1/admin/escrows", admin: true, alwaysOn: true, handler: s.handleAdminEscrows},
		{pattern: "/v1/admin/suspicious-hosts", label: "/v1/admin/suspicious-hosts", admin: true, alwaysOn: true, handler: s.handleAdminSuspiciousHosts},
		{pattern: "/v1/admin/filters", label: "/v1/admin/filters", admin: true, alwaysOn: true, handler: s.handleAdminFilters},
		{pattern: "/v1/admin/filters/presets", label: "/v1/admin/filters/presets", admin: true, alwaysOn: true, handler: s.handleAdminFilterPresets},
		{pattern: "/v1/admin/filters/{model...}", label: "/v1/admin/filters/{model}", admin: true, alwaysOn: true, handler: s.handleAdminModelFilter},
		{pattern: "/v1/admin/hosts", label: "/v1/admin/hosts", admin: true, alwaysOn: true, handler: s.handleAdminHosts},
		{pattern: "/v1/admin/participants/unquarantine", label: "/v1/admin/participants/unquarantine", admin: true, alwaysOn: true, handler: s.handleAdminUnquarantine},
		{pattern: "/v1/admin/accounting/reset/{epoch}", label: "/v1/admin/accounting/reset/{epoch}", admin: true, alwaysOn: true, handler: s.handleAdminResetAccountingEpoch},
		{pattern: "/v1/debug/rotation", label: "/v1/debug/rotation", admin: true, alwaysOn: true, handler: s.handleDebugRotation},
		{pattern: "/v1/debug/heightsync", label: "/v1/debug/heightsync", admin: true, alwaysOn: true, handler: s.handleDebugHeightSync},
		{pattern: "/v1/debug/memstats", label: "/v1/debug/memstats", admin: true, alwaysOn: true, handler: s.handleDebugMemstats},
		{pattern: "/debug/pprof/", admin: true, alwaysOn: true, handler: pprof.Index},
		{pattern: "/debug/pprof/cmdline", admin: true, alwaysOn: true, handler: pprof.Cmdline},
		{pattern: "/debug/pprof/profile", admin: true, alwaysOn: true, handler: pprof.Profile},
		{pattern: "/debug/pprof/symbol", admin: true, alwaysOn: true, handler: pprof.Symbol},
		{pattern: "/debug/pprof/trace", admin: true, alwaysOn: true, handler: pprof.Trace},

		{pattern: "/", label: otherRouteLabel, alwaysOn: true, handler: s.handleUnmatched},
	}
}

func (s *Server) handleUnmatched(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	for _, registered := range s.routes() {
		handler := registered.handler
		if registered.admin {
			handler = adminFailure(cmp.Or(registered.label, registered.pattern), s.requireAdmin(handler))
		}
		if !registered.alwaysOn {
			handler = s.disabled(handler)
		}
		var wrapped http.Handler = handler
		if registered.label != "" {
			wrapped = s.telemetry.InstrumentRoute(registered.label, wrapped)
		}
		mux.Handle(registered.pattern, wrapped)
	}
	return mux
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	s.telemetry.Handler().ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	writeJSON(w, http.StatusOK, s.modelList(s.escrows.Models()))
}

func (s *Server) handleDevshardModels(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	escrow, found := s.escrows.Routable(r.PathValue("id"))
	if !found {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, r.PathValue("id")))
		return
	}
	writeJSON(w, http.StatusOK, s.modelList([]string{escrow.Model}))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	writeJSON(w, http.StatusOK, s.status(s.routableEscrows()))
}

func (s *Server) handleDevshardStatus(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet, http.MethodHead) {
		return
	}
	escrow, found := s.escrows.Routable(r.PathValue("id"))
	if !found {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, r.PathValue("id")))
		return
	}
	writeJSON(w, http.StatusOK, s.status([]scheduler.Escrow{escrow}))
}

func (s *Server) routableEscrows() []scheduler.Escrow {
	models := s.escrows.Models()
	escrows := make([]scheduler.Escrow, 0, len(models))
	for _, model := range models {
		escrows = append(escrows, s.escrows.Candidates(model)...)
	}
	return escrows
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	s.chat(w, r, "")
}

// handleDevshardChat pins the race to one escrow, refusing a pin it no longer routes to. See operations.md, "What is exposed".
func (s *Server) handleDevshardChat(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	escrowID := r.PathValue("id")
	if _, routable := s.escrows.Routable(escrowID); !routable {
		s.writeErrorFor(w, fmt.Errorf("%w: %s", ErrUnknownDevshard, escrowID))
		return
	}
	s.chat(w, r, escrowID)
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, escrowPin string) {
	traced, span := observability.StartGatewayRequest(r.Context())
	defer span.End()
	r = r.WithContext(traced)
	configuration := s.config.Load()
	identity := s.resolveCredentials(r)
	requestID := s.requestIDs()

	body, err := readBody(w, r, chatIngestLimit)
	if err != nil {
		s.writeErrorFor(w, err)
		return
	}
	normalized, err := filters.NormalizeRequest(body, filterOptions(configuration.Limits, identity.admin, s.servedModelVocabulary, s.modelFilters.Profiles()))
	if err != nil {
		s.capture.filterRejected(r, requestID, body, err)
		s.writeErrorFor(w, err)
		return
	}
	if transport.PromptWireBytes(len(normalized.Body))+hostCatchUpReserveBytes > transport.MaxHostRequestBytes {
		s.writeErrorFor(w, tooLargeForHosts(len(normalized.Body)))
		return
	}
	if err := authorizeModel(configuration.Limits, normalized.Model, identity); err != nil {
		s.writeErrorFor(w, err)
		return
	}
	if err := s.routableModel(configuration.Limits, normalized.Model); err != nil {
		s.writeErrorFor(w, err)
		return
	}
	prompt, err := s.checkContext(configuration.Limits, normalized)
	if err != nil {
		s.writeErrorFor(w, err)
		return
	}
	refusal := admission(s.snapshots.Snapshot(), configuration.Modes, s.now(), configuration.Chain.SnapshotMaxAgeSeconds)
	if refusal != nil && (s.cache == nil || !blockedOnlyByPoC(refusal)) {
		s.writeErrorFor(w, refusal)
		return
	}

	var key cacheKey
	if s.cache != nil {
		key = cacheKeyFor(r, normalized)
		if entry, hit := s.cache.get(key, s.now()); hit {
			written := serveCached(w, requestID, entry)
			hitCount := prompt.countSoFar()
			s.events.RequestFinished(journal.RequestLine{
				RequestID: requestID, Model: normalized.Model, EscrowID: entry.escrowID,
				ClientStream: entry.stream, Bytes: written, CacheHit: true,
				Outcome: engine.RaceOutcome{InputTokens: hitCount.tokens, InputTokensSource: hitCount.source},
			})
			return
		}
	}
	if refusal != nil {
		s.writeErrorFor(w, refusal)
		return
	}

	counted := s.countPrompt(normalized.Model, prompt)
	if err := s.limiter.AcquireForModel(r.Context(), normalized.Model, int64(counted.tokens), s.capacity.ForModel(normalized.Model)); err != nil {
		if throttled, ours := rateLimited(err); ours && s.rejections != nil {
			s.rejections.Rejected(normalized.Model, throttled.Label())
			s.events.RequestThrottled(journal.RequestLine{
				RequestID: requestID, Model: normalized.Model, LimiterReason: throttled.Label(),
			})
		}
		s.writeErrorFor(w, err)
		return
	}
	slot := newLimiterSlot(func() { s.limiter.ReleaseForModel(normalized.Model, int64(counted.tokens)) })
	defer slot.done()
	// The limiter can hold a request for minutes: a block that began meanwhile refuses it before any nonce is spent.
	afterWait := s.config.Load()
	if refusal := admission(s.snapshots.Snapshot(), afterWait.Modes, s.now(), afterWait.Chain.SnapshotMaxAgeSeconds); refusal != nil {
		s.writeErrorFor(w, refusal)
		return
	}

	if s.cache == nil {
		// Nothing to store, so the outcome and the hidden failure the cache path reads are both moot.
		_, _ = s.race(w, r, requestID, normalized, counted, escrowPin, configuration.Limits.LogprobsOptimization, slot)
		return
	}
	recorder := newCacheRecorder(w, s.cache.entryLimit(), normalized.ClientStream, s.buffers)
	defer recorder.releaseBudget()
	outcome, hiddenFailure := s.race(recorder, r, requestID, normalized, counted, escrowPin, configuration.Limits.LogprobsOptimization, slot)
	unstorable := cmp.Or(r.Context().Err(), hiddenFailure)
	entry, storable := recorder.entry(outcome.EscrowID, normalized.ClientStream, unstorable)
	if !storable {
		s.countCacheResult(normalized.Model, recorder.entryRefusal(unstorable, outcome.EscrowID))
		return
	}
	// The client keeps every byte that arrived; only the replay stops, and only this refusal is news.
	cacheRefusal := s.cache.put(key, entry, s.now())
	s.countCacheResult(normalized.Model, cacheRefusal)
	if cacheRefusal == filters.CacheRefusedUnfinished {
		s.events.ReplyNotCached(journal.RequestLine{RequestID: requestID, Model: normalized.Model, EscrowID: outcome.EscrowID})
	}
}

func (s *Server) countCacheResult(model, refusal string) {
	if s.cacheResults == nil {
		return
	}
	s.cacheResults.CacheResult(model, cmp.Or(refusal, cacheResultStored))
}

func (s *Server) servedModelVocabulary(model string) int {
	if s.stopTokenVocabulary == nil || !s.escrows.Serves(model) {
		return filters.VocabularySizeUnknown
	}
	return s.stopTokenVocabulary(model)
}

// routableModel refuses a model before it can take a limiter slot. See README.md, "The route table".
func (s *Server) routableModel(configured config.Limits, model string) error {
	served := s.escrows.Models()
	if len(served) == 0 {
		return &ModelUnavailableError{Model: model}
	}
	if s.escrows.Serves(model) {
		return nil
	}
	if configured.Offers(model) {
		return &ModelUnavailableError{Model: model}
	}
	return &UnsupportedModelError{Model: model, Supported: served}
}

func authorizeModel(configured config.Limits, model string, identity credentials) error {
	if model == "" {
		return filters.Reject("model is required")
	}
	if identity.admin {
		return nil
	}
	switch configured.AccessFor(model) {
	case config.ModelAccessOpen:
		return nil
	case config.ModelAccessAPIKey:
		if identity.apiKey {
			return nil
		}
		return &AccessDeniedError{Model: model, Message: fmt.Sprintf("model %q requires an API key", model)}
	default:
		return &AccessDeniedError{Model: model, Message: fmt.Sprintf("model %q requires an admin API key", model)}
	}
}

// race returns the outcome and the failure the reply's status could not carry. See README.md, "Streaming the reply".
func (s *Server) race(w http.ResponseWriter, r *http.Request, requestID string, normalized filters.Result, prompt promptCount, escrowPin string, logprobsOptimization *bool, slot *limiterSlot) (engine.RaceOutcome, error) {
	startedAt := s.now()
	client := newClientStream(w, requestID, normalized.ClientStream, normalized.ClientUsage, normalized.Logprobs, s.buffers)
	defer client.discard()
	outputTokens := outputTokenBudget(normalized)
	outcome, err := s.inference.Run(r.Context(), engine.Request{
		RequestID:               requestID,
		Model:                   normalized.Model,
		Escrow:                  escrowPin,
		InputTokens:             prompt.tokens,
		InputTokensSource:       prompt.source,
		InputTokensFromBytes:    filters.EstimatedPromptTokens(len(normalized.Body)),
		InputBytes:              uint64(len(normalized.Body)),
		OutputTokens:            outputTokens,
		ClientStream:            normalized.ClientStream,
		EmptyAnswerCountsTokens: normalized.EmptyAnswerCountsTokens,
		OnEscrow:                func(escrowID string) { client.Header().Set(EscrowHeader, escrowID) },
		OnFinished:              slot.share(),

		Params: user.InferenceParams{
			Model:                        normalized.Model,
			Prompt:                       normalized.Body,
			InputLength:                  uint64(len(normalized.Body)),
			MaxTokens:                    outputTokens,
			StartedAt:                    s.now().Unix(),
			LogprobsOptimizationOverride: logprobsOptimization,
		},
	}, client)
	if err != nil {
		s.capture.attemptsFailed(r, requestID, normalized, err)
		if !client.closeUnstarted() {
			s.finishRequest(requestID, normalized, outcome, deliveryFailedMidStream, client, s.now().Sub(startedAt), err, client.Fail(err))
			return outcome, err
		}
		s.finishRequest(requestID, normalized, outcome, deliveryFailedBeforeFirstByte, client, s.now().Sub(startedAt), err, nil)
		w.Header().Set(RequestIDHeader, requestID)
		s.writeErrorFor(w, err)
		return outcome, nil
	}
	// The authoritative X-Devshard-ID write. See README.md, "Streaming the reply".
	if outcome.EscrowID != "" {
		client.Header().Set(EscrowHeader, outcome.EscrowID)
	}
	// Returned, not just logged, so the cache refuses a body that stops mid-answer.
	closeErr := client.Close()
	delivery := deliveryServed
	if closeErr != nil {
		delivery = deliveryFailedMidStream
	}
	s.finishRequest(requestID, normalized, outcome, delivery, client, s.now().Sub(startedAt), nil, closeErr)
	return outcome, closeErr
}
