package api

import (
	"net/http"

	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/httpx"
)

const pathAbort = "/trainshard/v0/admin/nodes/{node_id}/abort"

type Admin struct {
	participant vo.Participant
	abort       *usecases.AbortUseCase
}

func NewAdmin(participant vo.Participant, abort *usecases.AbortUseCase) *Admin {
	return &Admin{participant: participant, abort: abort}
}

func (a *Admin) Mount(mux *http.ServeMux) {
	mux.Handle("POST "+pathAbort, http.HandlerFunc(a.abortRun))
}

func (a *Admin) abortRun(w http.ResponseWriter, r *http.Request) {
	node, err := vo.ParseNodeRef(string(a.participant), r.PathValue("node_id"))
	if err != nil {
		httpx.WriteError(w, "", err)
		return
	}
	if err := a.abort.Execute(r.Context(), node); err != nil {
		httpx.WriteError(w, "", err)
		return
	}
	httpx.Write(w, "", struct{}{})
}
