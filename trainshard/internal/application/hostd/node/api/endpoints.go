package api

import (
	"net/http"

	usecases "trainshard/internal/application/hostd/node/use_cases"
	"trainshard/internal/contract"
	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/httpx"
)

type Endpoints struct {
	nodes     []vo.NodeRef
	version   string
	readiness *usecases.EvaluateReadinessUseCase
}

func NewEndpoints(nodes []vo.NodeRef, version string, readiness *usecases.EvaluateReadinessUseCase) *Endpoints {
	return &Endpoints{nodes: nodes, version: version, readiness: readiness}
}

func (e *Endpoints) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("GET "+contract.PathReadiness, guard(http.HandlerFunc(e.readinessReport)))
}

func (e *Endpoints) readinessReport(w http.ResponseWriter, r *http.Request) {
	results := make(map[vo.NodeRef]readiness.Result, len(e.nodes))
	for _, node := range e.nodes {
		results[node] = e.readiness.Execute(r.Context(), node)
	}
	httpx.Write(w, r.Header.Get(contract.HeaderRequestID), toReadinessOutput(e.nodes, e.version, results))
}
