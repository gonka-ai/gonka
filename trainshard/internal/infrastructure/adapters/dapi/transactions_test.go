package dapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

type txDapi struct {
	status int
	body   string
}

func (d txDapi) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || r.URL.Path != pathSendTx {
		return answer(http.StatusNotFound, nil), nil
	}
	return answer(d.status, []byte(d.body)), nil
}

func TestSendReadsARefusalOnlyByWhatTheDapiPassesAsFields(t *testing.T) {
	cases := []struct {
		name     string
		dapi     txDapi
		wantKind error
		wantCode string
	}{
		{"taken by the chain", txDapi{http.StatusOK, `{"txhash":"ABCD"}`}, nil, ""},
		{"refused by the inference module", txDapi{http.StatusOK, `{"codespace":"inference","code":1207,"raw_log":"only the creator may do this"}`}, shared.ErrConflict, "CHAIN_REFUSED"},
		{"already in the mempool", txDapi{http.StatusOK, `{"codespace":"sdk","code":19}`}, shared.ErrUnavailable, "CHAIN_REFUSED"},
		{"a code without its codespace", txDapi{http.StatusOK, `{"code":1207}`}, shared.ErrUnavailable, "CHAIN_REFUSED"},
		{"a refusal the dapi passes only as text", txDapi{http.StatusInternalServerError, `{"error":"checktx failed permanently: transaction ABCD failed: code=1207, codespace=inference, log=only the creator may do this"}`}, shared.ErrUnavailable, "DAPI_REFUSED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			client := New(&http.Client{Transport: tc.dapi}, Config{Address: "http://dapi", Participant: "gonka1host", Timeout: time.Second})

			// act
			err := client.OptIn(context.Background(), vo.NodeRef{NodeID: "node1"}, time.Minute)

			// assert
			if tc.wantKind == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, tc.wantKind) || shared.CodeOf(err) != tc.wantCode {
				t.Fatalf("got %v (%s), want %v %s", err, shared.CodeOf(err), tc.wantKind, tc.wantCode)
			}
		})
	}
}
