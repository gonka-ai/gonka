package observer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
)

var errCommitRejected = errors.New("commit not verified")

// cometJSONRPC is the Comet methods NewTendermint uses over HTTP.
type cometJSONRPC interface {
	Header(ctx context.Context, height *int64) (*ctypes.ResultHeader, error)
	Commit(ctx context.Context, height *int64) (*ctypes.ResultCommit, error)
	Validators(ctx context.Context, height *int64, page, perPage *int) (*ctypes.ResultValidators, error)
	BlockchainInfo(ctx context.Context, minHeight, maxHeight int64) (*ctypes.ResultBlockchainInfo, error)
}

// ServeJSONRPC serves c as Comet JSON-RPC for NewTendermint.
func (c *SignedChain) ServeJSONRPC() *httptest.Server {
	return ServeCometJSONRPC(c, nil)
}

// ServeCometJSONRPC serves rpc as Comet JSON-RPC. onCommit, when set, sees
// each commit height (0 when the client asked for the latest).
func ServeCometJSONRPC(rpc cometJSONRPC, onCommit func(int64)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeRPC(w, rpctypes.RPCParseError(err))
			return
		}
		var req rpctypes.RPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeRPC(w, rpctypes.RPCParseError(err))
			return
		}
		ctx := r.Context()
		params := rpcParams(req.Params)
		switch req.Method {
		case "commit":
			h := params.int64("height")
			if onCommit != nil {
				n := int64(0)
				if h != nil {
					n = *h
				}
				onCommit(n)
			}
			if rpc == nil {
				writeRPC(w, rpctypes.RPCInternalError(req.ID, errCommitRejected))
				return
			}
			res, err := rpc.Commit(ctx, h)
			writeRPCResult(w, req, res, err)
		case "header":
			if rpc == nil {
				writeRPC(w, rpctypes.RPCMethodNotFoundError(req.ID))
				return
			}
			res, err := rpc.Header(ctx, params.int64("height"))
			writeRPCResult(w, req, res, err)
		case "validators":
			if rpc == nil {
				writeRPC(w, rpctypes.RPCMethodNotFoundError(req.ID))
				return
			}
			res, err := rpc.Validators(ctx, params.int64("height"), params.int("page"), params.int("per_page"))
			writeRPCResult(w, req, res, err)
		case "blockchain":
			if rpc == nil {
				writeRPC(w, rpctypes.RPCMethodNotFoundError(req.ID))
				return
			}
			minH, maxH := int64(0), int64(0)
			if v := params.int64("minHeight"); v != nil {
				minH = *v
			}
			if v := params.int64("maxHeight"); v != nil {
				maxH = *v
			}
			res, err := rpc.BlockchainInfo(ctx, minH, maxH)
			writeRPCResult(w, req, res, err)
		default:
			writeRPC(w, rpctypes.RPCMethodNotFoundError(req.ID))
		}
	}))
}

func writeRPCResult(w http.ResponseWriter, req rpctypes.RPCRequest, res any, err error) {
	if err != nil {
		writeRPC(w, rpctypes.RPCInternalError(req.ID, err))
		return
	}
	writeRPC(w, rpctypes.NewRPCSuccessResponse(req.ID, res))
}

func writeRPC(w http.ResponseWriter, resp rpctypes.RPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type rpcParamMap map[string]json.RawMessage

func rpcParams(raw json.RawMessage) rpcParamMap {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func (p rpcParamMap) int64(key string) *int64 {
	if p == nil {
		return nil
	}
	raw, ok := p[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return &n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

func (p rpcParamMap) int(key string) *int {
	v := p.int64(key)
	if v == nil {
		return nil
	}
	n := int(*v)
	return &n
}
