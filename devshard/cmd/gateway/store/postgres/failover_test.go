package postgres

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"common/storage/pgtimeouts"
	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	failoverConnectTimeout = "500ms"
	failoverBound          = 3 * time.Second
	readOnlyProbe          = "show transaction_read_only"
)

type testServer struct {
	listener net.Listener
	mu       sync.Mutex
	conns    []net.Conn
	wg       sync.WaitGroup
}

func startTestServer(t *testing.T, serve func(net.Conn)) *testServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	server := &testServer{listener: listener}
	server.wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			server.conns = append(server.conns, conn)
			server.mu.Unlock()
			server.wg.Go(func() {
				serve(conn)
			})
		}
	})
	t.Cleanup(func() {
		listener.Close()
		server.mu.Lock()
		for _, conn := range server.conns {
			conn.Close()
		}
		server.mu.Unlock()
		server.wg.Wait()
	})
	return server
}

func (s *testServer) port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

func holdSilently(conn net.Conn) {
	buffer := make([]byte, 512)
	for {
		if _, err := conn.Read(buffer); err != nil {
			return
		}
	}
}

func servePostgres(conn net.Conn) {
	backend := pgproto3.NewBackend(conn, conn)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: 1})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	for {
		message, err := backend.Receive()
		if err != nil {
			return
		}
		query, isQuery := message.(*pgproto3.Query)
		if !isQuery {
			return
		}
		if strings.EqualFold(query.String, readOnlyProbe) {
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("transaction_read_only"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}})
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("off")}})
		}
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if backend.Flush() != nil {
			return
		}
	}
}

// Test flow:
//  1. Start a first host that accepts TCP and never answers, and a second host that speaks enough of the Postgres protocol to be writable.
//  2. Build the pool from a two-host connection string with the first host listed first and a connect and operation timeout of 500ms.
//  3. Run one write through the store's own `exec`.
//  4. Assert the write succeeds within a few seconds: the silent first host must not use up the operation deadline before the second host is tried.
func TestAWriteReachesTheSecondHostWhenTheFirstNeverAnswers(t *testing.T) {
	clearPostgresEnvironment(t)
	t.Setenv(pgtimeouts.EnvConnectTimeout, failoverConnectTimeout)
	t.Setenv(pgtimeouts.EnvOperationTimeout, failoverConnectTimeout)
	silent := startTestServer(t, holdSilently)
	writable := startTestServer(t, servePostgres)
	cfg, err := connectionConfig(fmt.Sprintf("host=127.0.0.1,127.0.0.1 port=%d,%d user=u dbname=gateway sslmode=disable", silent.port(), writable.port()))
	if err != nil {
		t.Fatalf("connectionConfig() = %v, want nil", err)
	}
	pool := openPool(cfg)
	t.Cleanup(func() { pool.Close() })
	db := database{pool: pool, operationTimeout: pgtimeouts.OperationTimeout()}

	started := time.Now()
	_, err = db.exec(context.Background(), `SELECT 1`)
	elapsed := time.Since(started)
	t.Logf("write through the second host took %v", elapsed)
	if err != nil {
		t.Fatalf("exec() after %v = %v, want a write through the second host", elapsed, err)
	}
	if elapsed > failoverBound {
		t.Fatalf("exec() took %v, want under %v", elapsed, failoverBound)
	}
}

// Test flow:
//  1. Build the connection config for one host, one host with a plaintext fallback, a host listed twice in a row, two and three hosts, two hosts with a plaintext fallback each and a host listed again after another, with a 2s connect timeout.
//  2. Assert a run of one host keeps the 2s connect timeout, and n consecutive different hosts get the smaller of 2s and the operation timeout divided by n+1.
//  3. Assert two hosts keep 2s when the operation timeout is 30s or disabled.
func TestTheConnectTimeoutLeavesRoomForEveryHostInTheOperation(t *testing.T) {
	clearPostgresEnvironment(t)
	t.Setenv(pgtimeouts.EnvConnectTimeout, "2s")
	for _, testCase := range []struct {
		name       string
		operation  string
		connection string
		want       time.Duration
	}{
		{"one host", "2s", "host=127.0.0.1 user=u dbname=gateway sslmode=disable", 2 * time.Second},
		{"one host with a plaintext fallback", "2s", "host=127.0.0.1 user=u dbname=gateway sslmode=prefer", 2 * time.Second},
		{"two hosts", "2s", "host=127.0.0.1,127.0.0.2 user=u dbname=gateway sslmode=disable", 2 * time.Second / 3},
		{"two hosts, each with a plaintext fallback", "2s", "host=127.0.0.1,127.0.0.2 user=u dbname=gateway sslmode=prefer", 2 * time.Second / 3},
		{"one host listed twice in a row", "2s", "host=127.0.0.1,127.0.0.1 user=u dbname=gateway sslmode=disable", 2 * time.Second},
		{"a host listed again after another", "2s", "host=127.0.0.1,127.0.0.2,127.0.0.1 user=u dbname=gateway sslmode=disable", 500 * time.Millisecond},
		{"three hosts", "2s", "host=127.0.0.1,127.0.0.2,127.0.0.3 user=u dbname=gateway sslmode=disable", 500 * time.Millisecond},
		{"two hosts, a long operation", "30s", "host=127.0.0.1,127.0.0.2 user=u dbname=gateway sslmode=disable", 2 * time.Second},
		{"two hosts, no operation deadline", "0", "host=127.0.0.1,127.0.0.2 user=u dbname=gateway sslmode=disable", 2 * time.Second},
	} {
		t.Setenv(pgtimeouts.EnvOperationTimeout, testCase.operation)
		cfg, err := connectionConfig(testCase.connection)
		if err != nil {
			t.Fatalf("%s: connectionConfig() = %v, want nil", testCase.name, err)
		}
		if got := cfg.ConnConfig.ConnectTimeout; got != testCase.want {
			t.Errorf("%s: connect timeout = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
