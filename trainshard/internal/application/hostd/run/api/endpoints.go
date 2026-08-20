package api

import (
	"encoding/json"
	"net/http"

	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/contract"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/httpx"
)

var errBadJSON = shared.New("BAD_BODY", shared.ErrValidation, "cannot decode request body")

type UseCases struct {
	Deploy     *usecases.DeployUseCase
	Start      *usecases.StartUseCase
	Stop       *usecases.StopUseCase
	Status     *usecases.StatusUseCase
	Report     *usecases.ReportUseCase
	Mesh       *usecases.ApplyMeshUseCase
	Identities *usecases.CollectIdentitiesUseCase
	Probe      *usecases.ProbeMeshUseCase
}

type Endpoints struct {
	participant vo.Participant
	uc          UseCases
}

func NewEndpoints(participant vo.Participant, uc UseCases) *Endpoints {
	return &Endpoints{participant: participant, uc: uc}
}

func (e *Endpoints) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	routes := map[string]http.HandlerFunc{
		"POST " + contract.PathMesh:   e.applyMesh,
		"POST " + contract.PathDeploy: e.deployRun,
		"POST " + contract.PathStart:  e.startRun,
		"POST " + contract.PathStop:   e.stopRun,
		"POST " + contract.PathStatus: e.runStatus,
		"POST " + contract.PathReport: e.runReport,
	}
	for pattern, handler := range routes {
		mux.Handle(pattern, guard(handler))
	}
	mux.Handle("GET "+contract.PathMesh, guard(http.HandlerFunc(e.meshIdentities)))
	mux.Handle("POST "+contract.PathProbe, guard(http.HandlerFunc(e.probeMesh)))
}

func (e *Endpoints) probeMesh(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r.Context())

	shardID, node, err := toNodePath(e.participant, r.PathValue("shard_id"), r.PathValue("node_id"))
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}
	unreachable, err := e.uc.Probe.Execute(r.Context(), shardID, node, actorFrom(r.Context()))
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}
	httpx.Write(w, requestID, toProbeOutput(node, unreachable))
}

func (e *Endpoints) meshIdentities(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFrom(r.Context())

	shardID, err := vo.ParseShardID(r.PathValue("shard_id"))
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}

	identities, err := e.uc.Identities.Execute(r.Context(), shardID, actorFrom(r.Context()))
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}
	httpx.Write(w, requestID, toMeshOutput(identities))
}

func (e *Endpoints) deployRun(w http.ResponseWriter, r *http.Request) {
	serve(w, r, func(dto contract.DeployRequest) (contract.NodesResult, error) {
		cmd, err := toDeployCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), dto)
		if err != nil {
			return contract.NodesResult{}, err
		}
		results, err := e.uc.Deploy.Execute(r.Context(), cmd)
		if err != nil {
			return contract.NodesResult{}, err
		}
		return toNodesOutput(results), nil
	})
}

func (e *Endpoints) startRun(w http.ResponseWriter, r *http.Request) {
	serve(w, r, func(dto contract.StartRequest) (contract.NodesResult, error) {
		cmd, err := toNodesCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), dto.Command)
		if err != nil {
			return contract.NodesResult{}, err
		}
		results, err := e.uc.Start.Execute(r.Context(), cmd)
		if err != nil {
			return contract.NodesResult{}, err
		}
		return toNodesOutput(results), nil
	})
}

func (e *Endpoints) stopRun(w http.ResponseWriter, r *http.Request) {
	serve(w, r, func(dto contract.StopRequest) (contract.NodesResult, error) {
		cmd, err := toStopCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), dto)
		if err != nil {
			return contract.NodesResult{}, err
		}
		results, err := e.uc.Stop.Execute(r.Context(), cmd)
		if err != nil {
			return contract.NodesResult{}, err
		}
		return toNodesOutput(results), nil
	})
}

func (e *Endpoints) runStatus(w http.ResponseWriter, r *http.Request) {
	serve(w, r, func(dto contract.StatusRequest) (contract.StatusResult, error) {
		cmd, err := toNodesCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), dto.Command)
		if err != nil {
			return contract.StatusResult{}, err
		}
		statuses, err := e.uc.Status.Execute(r.Context(), cmd)
		if err != nil {
			return contract.StatusResult{}, err
		}
		return toStatusOutput(statuses), nil
	})
}

func (e *Endpoints) runReport(w http.ResponseWriter, r *http.Request) {
	serve(w, r, func(dto contract.ReportRequest) (contract.ReportResult, error) {
		cmd, err := toNodesCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), dto.Command)
		if err != nil {
			return contract.ReportResult{}, err
		}
		reports, err := e.uc.Report.Execute(r.Context(), cmd)
		if err != nil {
			return contract.ReportResult{}, err
		}
		return toReportOutput(reports), nil
	})
}

func (e *Endpoints) applyMesh(w http.ResponseWriter, r *http.Request) {
	serve(w, r, func(dto contract.MeshRequest) (contract.NodesResult, error) {
		cmd, err := toMeshCommand(e.participant, actorFrom(r.Context()), r.PathValue("shard_id"), dto)
		if err != nil {
			return contract.NodesResult{}, err
		}
		results, err := e.uc.Mesh.Execute(r.Context(), cmd)
		if err != nil {
			return contract.NodesResult{}, err
		}
		return toNodesOutput(results), nil
	})
}

func serve[Request, Result any](w http.ResponseWriter, r *http.Request, run func(Request) (Result, error)) {
	requestID := requestIDFrom(r.Context())

	var dto Request
	if err := decode(r, &dto); err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}
	result, err := run(dto)
	if err != nil {
		httpx.WriteError(w, requestID, err)
		return
	}
	httpx.Write(w, requestID, result)
}

func decode(r *http.Request, dto any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dto); err != nil {
		return errBadJSON
	}
	return nil
}
