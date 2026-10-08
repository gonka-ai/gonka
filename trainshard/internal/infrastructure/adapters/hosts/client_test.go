package hosts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/timex"
)

type signerStub struct{}

func (signerStub) Sign([]byte) []byte { return []byte("signature") }

func TestAnAnswerAHostCannotBackIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		answer string
	}{
		{"an envelope past the answer limit", `{"ok":true,"data":{"items":[]},"meta":{"request_id":"` + strings.Repeat("x", maxAnswerBytes) + `"}}`},
		{"data that is not the result asked for", `{"ok":true,"data":{"items":"none"},"meta":{"request_id":"req-1"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.answer))
			}))
			t.Cleanup(server.Close)
			clock := timex.NewFrozen(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
			client := New(server.Client(), signerStub{}, clock, time.Minute)
			host := vo.Host{Participant: "gonka1host", Endpoint: vo.Endpoint(server.URL)}
			call := run.HostCommand{Shard: 7, RequestID: "req-1", Deadline: clock.Now().Add(time.Minute)}

			// act
			_, err := client.Status(context.Background(), host, call)

			// assert
			if !errors.Is(err, shared.ErrUnavailable) || shared.CodeOf(err) != "HOST_ANSWER" {
				t.Fatalf("got %v (%s), want HOST_ANSWER", err, shared.CodeOf(err))
			}
		})
	}
}
