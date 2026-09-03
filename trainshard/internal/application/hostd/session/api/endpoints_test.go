package api_test

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"trainshard/internal/application/hostd/session"
	"trainshard/internal/contract"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/utils/signedhttp"
	"trainshard/internal/utils/timex"
)

const (
	participant = vo.Participant("gonka1host")
	shardID     = vo.ShardID(7)
)

type verifierStub struct{}

func (verifierStub) Recover(_, signature []byte) (vo.Address, error) {
	return vo.Address(signature), nil
}

type servedStub struct {
	spent map[string]bool
}

func (s servedStub) First(_ context.Context, request string, _ time.Time) (bool, error) {
	if s.spent[request] {
		return false, nil
	}
	s.spent[request] = true
	return true, nil
}

var (
	now   = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	nodeA = vo.NodeRef{Participant: participant, NodeID: "node-a"}
)

type chainStub struct {
	record shard.Shard
}

func newChainStub() *chainStub {
	return &chainStub{record: shard.Shard{
		ID:              shardID,
		Creator:         "gonka1creator",
		Status:          shard.StatusActive,
		ExpiresAtHeight: 1000,
		Nodes:           []shard.ReservedNode{{Ref: nodeA}},
	}}
}

func (c *chainStub) Height(context.Context) (vo.Height, error) { return 500, nil }

func (c *chainStub) Shard(context.Context, vo.ShardID) (shard.Shard, bool, error) {
	return c.record, true, nil
}

func (c *chainStub) Reservation(context.Context, vo.NodeRef) (vo.ShardID, bool, error) {
	return shardID, true, nil
}

func (c *chainStub) ActiveShards(context.Context) ([]shard.Shard, error) { return nil, nil }

func (c *chainStub) Hardware(context.Context, vo.NodeRef) (vo.GPUInventory, error) {
	return vo.GPUInventory{}, nil
}

type sessionLogStub struct{}

func (sessionLogStub) Record(context.Context, vo.ShardID, vo.NodeRef, time.Time) (io.WriteCloser, error) {
	return nopSink{}, nil
}

type nopSink struct{}

func (nopSink) Write(p []byte) (int, error) { return len(p), nil }

func (nopSink) Close() error { return nil }

type streamsStub struct {
	output string
}

func (s *streamsStub) Logs(_ context.Context, _ run.LogRequest, out io.Writer) error {
	_, err := io.WriteString(out, s.output)
	return err
}

func (s *streamsStub) Shell(_ context.Context, _ run.ExecRequest, terminal io.ReadWriter) error {
	line, err := bufio.NewReader(terminal).ReadString('\n')
	if err != nil {
		return err
	}
	_, err = io.WriteString(terminal, "you said "+line)
	return err
}

func newServer(t *testing.T, chain *chainStub, streams *streamsStub) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	module := session.New(session.Config{Participant: participant}, session.Deps{
		Chain:    chain,
		Streams:  streams,
		Sessions: sessionLogStub{},
		Served:   servedStub{spent: map[string]bool{}},
		Clock:    timex.NewFrozen(now),
	})
	module.Mount(mux, signedhttp.New(verifierStub{}, timex.NewFrozen(now), time.Minute, vo.Address(participant)).Wrap)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func sign(t *testing.T, actor vo.Address) http.Header {
	t.Helper()

	header := http.Header{}
	header.Set(contract.HeaderTimestamp, now.Format(time.RFC3339))
	header.Set(contract.HeaderRequestID, "req-1")
	header.Set(contract.HeaderSignature, hex.EncodeToString([]byte(actor)))
	return header
}

func TestLogsAreStreamedToWhoeverOwnsTheRun(t *testing.T) {
	// arrange
	server := newServer(t, newChainStub(), &streamsStub{output: "step 1\nstep 2\n"})
	path := "/trainshard/v0/shards/7/nodes/node-a/logs"
	request, _ := http.NewRequest(http.MethodPost, server.URL+path, nil)
	request.Header = sign(t, "gonka1creator")

	// act
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)

	// assert
	if response.StatusCode != http.StatusOK {
		t.Fatalf("got %d: %s", response.StatusCode, body)
	}
	if string(body) != "step 1\nstep 2\n" {
		t.Fatalf("got %q, want the container output verbatim", body)
	}
}

func TestAStreamIsOpenedOnceForTheRequestThatAskedForIt(t *testing.T) {
	// arrange
	server := newServer(t, newChainStub(), &streamsStub{output: "step 1\n"})
	path := "/trainshard/v0/shards/7/nodes/node-a/logs"
	ask := func() *http.Response {
		request, _ := http.NewRequest(http.MethodPost, server.URL+path, nil)
		request.Header = sign(t, "gonka1creator")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		t.Cleanup(func() { response.Body.Close() })
		return response
	}
	if first := ask(); first.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want the first request through", first.StatusCode)
	}

	// act
	repeat := ask()

	// assert
	if repeat.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want a caught request refused the second time it arrives", repeat.StatusCode)
	}
	var envelope contract.Envelope
	if err := json.NewDecoder(repeat.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.OK || envelope.Error.Code != "REPLAYED_REQUEST" {
		t.Fatalf("got %+v, want the reason the repeat was refused", envelope.Error)
	}
}

func TestARefusedStreamAnswersWithAnErrorAndNoOutput(t *testing.T) {
	// arrange
	chain := newChainStub()
	chain.record.Status = shard.StatusSettled
	server := newServer(t, chain, &streamsStub{output: "secret"})
	path := "/trainshard/v0/shards/7/nodes/node-a/logs"
	request, _ := http.NewRequest(http.MethodPost, server.URL+path, nil)
	request.Header = sign(t, "gonka1creator")

	// act
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()

	// assert
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("got %d, want the request refused", response.StatusCode)
	}
	var envelope contract.Envelope
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope.OK || envelope.Error.Code != "SHARD_CLOSED" {
		t.Fatalf("got %+v, want the reason the stream was refused", envelope.Error)
	}
}

func TestShellCarriesBytesBothWaysOverOneConnection(t *testing.T) {
	// arrange
	server := newServer(t, newChainStub(), &streamsStub{})
	path := "/trainshard/v0/shards/7/nodes/node-a/shell"
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	request, _ := http.NewRequest(http.MethodPost, server.URL+path, nil)
	request.Header = sign(t, "gonka1creator")
	if err := request.Write(conn); err != nil {
		t.Fatalf("write request: %v", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	// act
	if _, err := fmt.Fprintln(conn, "whoami"); err != nil {
		t.Fatalf("type into the shell: %v", err)
	}
	answer, err := reader.ReadString('\n')

	// assert
	if response.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want the connection handed over", response.StatusCode)
	}
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if answer != "you said whoami\n" {
		t.Fatalf("got %q, want the container's answer", answer)
	}
}
