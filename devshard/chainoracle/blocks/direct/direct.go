package direct

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"common/chain"
	"common/chainoracle/blocks"

	"github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"google.golang.org/grpc"
)

// Fetcher is one hash-only block source (gRPC or Comet RPC).
type Fetcher interface {
	Latest(ctx context.Context) (*blocks.Header, error)
	At(ctx context.Context, height int64) (*blocks.Header, error)
}

// Oracle prefers primary (gRPC) and falls back to secondary (Comet RPC).
// Commit is always empty (hash-only; Strong needs LightBlock).
type Oracle struct {
	primary   Fetcher
	secondary Fetcher
}

// New constructs a hash-only direct-chain oracle. Either fetcher may be nil.
func New(primary, secondary Fetcher) *Oracle {
	return &Oracle{primary: primary, secondary: secondary}
}

// NewFromChain uses direct gRPC GetLatestBlock as primary. rpcURL, when set,
// is Comet HTTP RPC used when gRPC is missing. GetLatestBlock is not an ABCI
// query, so this does not ride chain.Client's query-fallback conn.
func NewFromChain(c *chain.Client, rpcURL string) *Oracle {
	var primary, secondary Fetcher
	if c != nil {
		primary = NewGRPCFetcher(cmtservice.NewServiceClient(c.Conn()))
	}
	if u := strings.TrimSpace(rpcURL); u != "" {
		secondary = NewRPCFetcher(u, nil)
	}
	return New(primary, secondary)
}

func (o *Oracle) Latest(ctx context.Context) (*blocks.Header, error) {
	return o.fetch(ctx, func(f Fetcher) (*blocks.Header, error) { return f.Latest(ctx) })
}

func (o *Oracle) At(ctx context.Context, height int64) (*blocks.Header, error) {
	return o.fetch(ctx, func(f Fetcher) (*blocks.Header, error) { return f.At(ctx, height) })
}

func (o *Oracle) fetch(ctx context.Context, fn func(Fetcher) (*blocks.Header, error)) (*blocks.Header, error) {
	var primaryErr error
	if o != nil && o.primary != nil {
		h, err := fn(o.primary)
		if err == nil && h != nil {
			return h, nil
		}
		primaryErr = err
	}
	if o != nil && o.secondary != nil {
		h, err := fn(o.secondary)
		if err == nil && h != nil {
			return h, nil
		}
		if primaryErr == nil {
			primaryErr = err
		}
	}
	if primaryErr != nil {
		return nil, primaryErr
	}
	return nil, fmt.Errorf("direct chain: no block source")
}

func (o *Oracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *Oracle) Subscribe(ctx context.Context, fromHeight int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header, 1)
	go func() {
		defer close(ch)
		h, err := o.Latest(ctx)
		if err == nil && h != nil && h.Height >= fromHeight {
			select {
			case <-ctx.Done():
			case ch <- h:
			}
		}
		<-ctx.Done()
	}()
	return ch, nil
}

// cometBlocks is the slice of cmtservice.ServiceClient used for hash-only headers.
type cometBlocks interface {
	GetLatestBlock(ctx context.Context, in *cmtservice.GetLatestBlockRequest, opts ...grpc.CallOption) (*cmtservice.GetLatestBlockResponse, error)
	GetBlockByHeight(ctx context.Context, in *cmtservice.GetBlockByHeightRequest, opts ...grpc.CallOption) (*cmtservice.GetBlockByHeightResponse, error)
}

// GRPCFetcher reads GetLatestBlock / GetBlockByHeight.
type GRPCFetcher struct {
	cli cometBlocks
}

func NewGRPCFetcher(cli cometBlocks) *GRPCFetcher {
	return &GRPCFetcher{cli: cli}
}

func (f *GRPCFetcher) Latest(ctx context.Context) (*blocks.Header, error) {
	if f == nil || f.cli == nil {
		return nil, fmt.Errorf("direct chain: nil comet grpc client")
	}
	resp, err := f.cli.GetLatestBlock(ctx, &cmtservice.GetLatestBlockRequest{})
	if err != nil {
		return nil, err
	}
	return headerFromSDKBlock(resp.GetBlockId(), resp.GetSdkBlock())
}

func (f *GRPCFetcher) At(ctx context.Context, height int64) (*blocks.Header, error) {
	if f == nil || f.cli == nil {
		return nil, fmt.Errorf("direct chain: nil comet grpc client")
	}
	resp, err := f.cli.GetBlockByHeight(ctx, &cmtservice.GetBlockByHeightRequest{Height: height})
	if err != nil {
		return nil, err
	}
	return headerFromSDKBlock(resp.GetBlockId(), resp.GetSdkBlock())
}

func headerFromSDKBlock(id *types.BlockID, sdkBlock *cmtservice.Block) (*blocks.Header, error) {
	if sdkBlock == nil {
		return nil, fmt.Errorf("direct chain: empty comet header")
	}
	hdr := sdkBlock.Header
	var hash []byte
	if id != nil {
		hash = append([]byte(nil), id.Hash...)
	}
	return blocks.HashOnlyHeader(hdr.Height, hdr.Time, hdr.ChainID, hash), nil
}

// RPCFetcher reads CometBFT JSON-RPC GET /block.
type RPCFetcher struct {
	base string
	hc   *http.Client
}

func NewRPCFetcher(baseURL string, hc *http.Client) *RPCFetcher {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	return &RPCFetcher{base: strings.TrimRight(strings.TrimSpace(baseURL), "/"), hc: hc}
}

func (f *RPCFetcher) Latest(ctx context.Context) (*blocks.Header, error) {
	return f.get(ctx, "")
}

func (f *RPCFetcher) At(ctx context.Context, height int64) (*blocks.Header, error) {
	return f.get(ctx, strconv.FormatInt(height, 10))
}

func (f *RPCFetcher) get(ctx context.Context, height string) (*blocks.Header, error) {
	if f == nil || f.base == "" {
		return nil, fmt.Errorf("direct chain: empty comet rpc url")
	}
	u := f.base + "/block"
	if height != "" {
		u += "?height=" + height
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("direct chain: GET %s: status %d", u, resp.StatusCode)
	}
	return parseCometBlockJSON(body)
}

type cometBlockJSON struct {
	Result struct {
		BlockID struct {
			Hash string `json:"hash"`
		} `json:"block_id"`
		Block struct {
			Header struct {
				ChainID string `json:"chain_id"`
				Height  string `json:"height"`
				Time    string `json:"time"`
			} `json:"header"`
		} `json:"block"`
	} `json:"result"`
}

func parseCometBlockJSON(body []byte) (*blocks.Header, error) {
	var parsed cometBlockJSON
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("direct chain: decode block json: %w", err)
	}
	height, err := strconv.ParseInt(parsed.Result.Block.Header.Height, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("direct chain: height: %w", err)
	}
	var ts time.Time
	if raw := parsed.Result.Block.Header.Time; raw != "" {
		ts, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, raw)
			if err != nil {
				return nil, fmt.Errorf("direct chain: time: %w", err)
			}
		}
	}
	hash, err := decodeHex(parsed.Result.BlockID.Hash)
	if err != nil {
		return nil, err
	}
	return blocks.HashOnlyHeader(height, ts, parsed.Result.Block.Header.ChainID, hash), nil
}

func decodeHex(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return nil, fmt.Errorf("direct chain: empty block hash")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("direct chain: block hash: %w", err)
	}
	return b, nil
}
