package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"trainshard/internal/application/hostd/run/api"
	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/domain/shared/vo"
)

func TestAbortReleasesOnlyANodeThisDaemonServes(t *testing.T) {
	cases := []struct {
		name     string
		node     string
		status   int
		released bool
	}{
		{name: "a node this daemon serves", node: "node-a", status: http.StatusOK, released: true},
		{name: "a node the participant keeps on another machine", node: "node-c", status: http.StatusUnprocessableEntity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			submitter := &submitterStub{}
			mux := http.NewServeMux()
			api.NewAdmin(host, usecases.NewAbortUseCase(chainStub{}, submitter)).Mount(mux)
			response := httptest.NewRecorder()

			// act
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/trainshard/v0/admin/nodes/"+tc.node+"/abort", nil))

			// assert
			if response.Code != tc.status {
				t.Fatalf("got %d %s, want %d", response.Code, response.Body, tc.status)
			}
			want := []vo.NodeRef{}
			if tc.released {
				want = []vo.NodeRef{{Participant: participant, NodeID: vo.NodeID(tc.node)}}
			}
			if len(submitter.released) != len(want) || (len(want) == 1 && submitter.released[0] != want[0]) {
				t.Fatalf("got %v released, want %v", submitter.released, want)
			}
		})
	}
}
