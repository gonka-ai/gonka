package api

import (
	"encoding/json"
	"net/http"

	usecases "trainshard/internal/application/hostd/session/use_cases"
	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/httpx"
	"trainshard/internal/utils/signedhttp"
)

var errBadJSON = shared.New("BAD_BODY", shared.ErrValidation, "cannot decode request body")

type UseCases struct {
	Logs      *usecases.StreamLogsUseCase
	Shell     *usecases.OpenShellUseCase
	Artifacts *usecases.StreamArtifactsUseCase
}

type Endpoints struct {
	participant vo.Participant
	uc          UseCases
	once        *signedhttp.Once
}

func NewEndpoints(participant vo.Participant, uc UseCases, once *signedhttp.Once) *Endpoints {
	return &Endpoints{participant: participant, uc: uc, once: once}
}

// Mount serves each session request once: a stream cannot be recorded and replayed the way a
// command's answer is, so a repeat would open a second shell rather than answer the first again
func (e *Endpoints) Mount(mux *http.ServeMux, boundary func(http.Handler) http.Handler) {
	served := func(h http.HandlerFunc) http.Handler { return boundary(e.once.Wrap(h)) }
	mux.Handle("POST "+contract.PathLogs, served(e.streamLogs))
	mux.Handle("POST "+contract.PathShell, served(e.openShell))
	mux.Handle("POST "+contract.PathArtifacts, served(e.streamArtifacts))
}

func (e *Endpoints) streamArtifacts(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r.Context())

	cmd, err := toSessionCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), r.PathValue("node_id"))
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}

	out := &stream{writer: w}
	if err := e.uc.Artifacts.Execute(r.Context(), cmd, out); err != nil && !out.started {
		httpx.WriteError(w, requestID, err)
	}
}

func (e *Endpoints) streamLogs(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r.Context())

	var dto contract.LogsRequest
	if err := decode(r, &dto); err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}
	cmd, err := toLogsCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), r.PathValue("node_id"), dto)
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}

	out := &stream{writer: w}
	if err := e.uc.Logs.Execute(r.Context(), cmd, out); err != nil && !out.started {
		httpx.WriteError(w, requestID, err)
	}
}

func (e *Endpoints) openShell(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r.Context())

	cmd, err := toSessionCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), r.PathValue("node_id"))
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}

	session := &duplex{writer: w}
	defer session.close()
	if err := e.uc.Shell.Execute(r.Context(), cmd, session); err != nil && !session.started {
		httpx.WriteError(w, requestID, err)
	}
}

func decode(r *http.Request, dto any) error {
	if r.ContentLength == 0 {
		return nil
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dto); err != nil {
		return errBadJSON
	}
	return nil
}
