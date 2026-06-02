package main

import (
	"strings"
	"testing"
)

func TestExtractChainRPCMethod(t *testing.T) {
	tests := []struct {
		name    string
		request string
		method  string
		ok      bool
	}{
		{
			name:    "path method",
			request: "GET /chain-rpc/status HTTP/1.1",
			method:  "status",
			ok:      true,
		},
		{
			name:    "path method with query",
			request: "GET /chain-rpc/block?height=123 HTTP/1.1",
			method:  "block",
			ok:      true,
		},
		{
			name:    "chain rpc root",
			request: "POST /chain-rpc/ HTTP/1.1",
			method:  "-",
			ok:      true,
		},
		{
			name:    "sanitizes method",
			request: "GET /chain-rpc/a%20b HTTP/1.1",
			method:  "a_b",
			ok:      true,
		},
		{
			name:    "non chain rpc",
			request: "GET /api/v1/models HTTP/1.1",
			ok:      false,
		},
		{
			name:    "malformed request",
			request: "/chain-rpc/status",
			ok:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, ok := extractChainRPCMethod(tt.request)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if method != tt.method {
				t.Fatalf("method = %q, want %q", method, tt.method)
			}
		})
	}
}

func TestExtractJSONRPCMethods(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		methods   []string
		batchSize int
		ok        bool
	}{
		{
			name:      "single request",
			body:      `{"jsonrpc":"2.0","id":1,"method":"status","params":{}}`,
			methods:   []string{"status"},
			batchSize: 1,
			ok:        true,
		},
		{
			name:      "batch request",
			body:      `[{"jsonrpc":"2.0","id":1,"method":"status"},{"jsonrpc":"2.0","id":2,"method":"block"}]`,
			methods:   []string{"status", "block"},
			batchSize: 2,
			ok:        true,
		},
		{
			name:      "sanitizes method",
			body:      `{"jsonrpc":"2.0","id":1,"method":"bad method"}`,
			methods:   []string{"bad_method"},
			batchSize: 1,
			ok:        true,
		},
		{
			name: "empty body",
			body: "",
			ok:   false,
		},
		{
			name: "invalid json",
			body: "{",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			methods, batchSize, ok := extractJSONRPCMethods([]byte(tt.body))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if batchSize != tt.batchSize {
				t.Fatalf("batchSize = %d, want %d", batchSize, tt.batchSize)
			}
			if len(methods) != len(tt.methods) {
				t.Fatalf("methods = %v, want %v", methods, tt.methods)
			}
			for i := range methods {
				if methods[i] != tt.methods[i] {
					t.Fatalf("methods = %v, want %v", methods, tt.methods)
				}
			}
		})
	}
}

func TestExtractJSONRPCLogItemsRedactsParams(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"abci_query","params":{"path":"/cosmos.bank.v1beta1.Query/Balance","data":"Ci1pbmZlcmVuY2UxYWJj","height":"123","prove":false}}`)

	items, batchSize, ok := extractJSONRPCLogItems(body)
	if !ok {
		t.Fatal("expected JSON-RPC log item")
	}
	if batchSize != 1 {
		t.Fatalf("batchSize = %d, want 1", batchSize)
	}
	if len(items) != 1 {
		t.Fatalf("items = %v, want one item", items)
	}
	if items[0].Method != "abci_query" {
		t.Fatalf("method = %q, want abci_query", items[0].Method)
	}

	wantParts := []string{
		"path=/cosmos.bank.v1beta1.Query/Balance",
		"data_len=20",
		"data_sha256=",
		"height=123",
		"prove=false",
	}
	for _, part := range wantParts {
		if !strings.Contains(items[0].Params, part) {
			t.Fatalf("params = %q, missing %q", items[0].Params, part)
		}
	}
	if strings.Contains(items[0].Params, "Ci1pbmZlcmVuY2UxYWJj") {
		t.Fatalf("params leaked raw data: %q", items[0].Params)
	}
}

func TestExtractJSONRPCLogItemsRedactsTxPayload(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"broadcast_tx_sync","params":{"tx":"signed-transaction-with-signature"}}`)

	items, _, ok := extractJSONRPCLogItems(body)
	if !ok {
		t.Fatal("expected JSON-RPC log item")
	}
	if items[0].Params == "" || !strings.Contains(items[0].Params, "tx_len=33") || !strings.Contains(items[0].Params, "tx_sha256=") {
		t.Fatalf("params = %q, want redacted tx summary", items[0].Params)
	}
	if strings.Contains(items[0].Params, "signed-transaction") || strings.Contains(items[0].Params, "signature") {
		t.Fatalf("params leaked tx payload: %q", items[0].Params)
	}
}
