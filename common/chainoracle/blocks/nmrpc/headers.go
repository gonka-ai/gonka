package nmrpc

import (
	"context"
	"errors"
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"
	"common/runtimeconfig"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetBlockHeaders serves NodeManager.GetBlockHeaders: At() catch-up then
// Subscribe wait. from_height is exclusive.
func GetBlockHeaders(ctx context.Context, oracle blocks.BlockOracle, req *gen.GetBlockHeadersRequest) (*gen.GetBlockHeadersResponse, error) {
	if oracle == nil {
		return nil, status.Error(codes.FailedPrecondition, "block headers: oracle not configured")
	}
	var (
		from       int64
		maxWaitSec int32
		maxHeaders uint32
	)
	if req != nil {
		from = req.GetFromHeight()
		maxWaitSec = req.GetMaxWaitSeconds()
		maxHeaders = req.GetMaxHeaders()
	}
	if from < 0 {
		return nil, status.Error(codes.InvalidArgument, "block headers: negative from_height")
	}

	latest, err := oracle.Latest(ctx)
	if err != nil {
		return nil, statusFromOracleErr(err)
	}
	if latest == nil || blocks.IsDummyHeader(latest) {
		return nil, status.Error(codes.NotFound, blocks.ErrHeaderNotFound.Error())
	}

	tip := latest.Height
	oldest := advertisedOldest(oracle, tip)
	effective := clampFromHeight(from, oldest)
	limit := clampMaxHeaders(maxHeaders)
	maxWait := runtimeconfig.ClampMaxWait(maxWaitSec, 0)

	var (
		wake   <-chan *blocks.Header
		cancel context.CancelFunc
	)
	if maxWait > 0 {
		subCtx, c := context.WithCancel(ctx)
		cancel = c
		ch, subErr := oracle.Subscribe(subCtx, effective+1)
		if subErr != nil {
			cancel()
			return nil, statusFromOracleErr(subErr)
		}
		wake = ch
	}
	if cancel != nil {
		defer cancel()
	}

	latest, err = oracle.Latest(ctx)
	if err != nil {
		return nil, statusFromOracleErr(err)
	}
	if latest == nil || blocks.IsDummyHeader(latest) {
		return nil, status.Error(codes.NotFound, blocks.ErrHeaderNotFound.Error())
	}
	tip = latest.Height
	oldest = advertisedOldest(oracle, tip)

	headers, last, err := catchUp(ctx, oracle, effective, tip, limit)
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		return headersResponse(headers, last, oldest, tip), nil
	}
	if maxWait <= 0 {
		return unchangedResponse(effective, oldest, tip), nil
	}

	notified, waitErr := waitHeader(ctx, wake, maxWait)
	if waitErr != nil {
		return nil, status.FromContextError(waitErr).Err()
	}
	if !notified {
		latest, err = oracle.Latest(ctx)
		if err != nil {
			return nil, statusFromOracleErr(err)
		}
		if latest != nil && !blocks.IsDummyHeader(latest) {
			tip = latest.Height
			oldest = advertisedOldest(oracle, tip)
		}
		return unchangedResponse(effective, oldest, tip), nil
	}

	latest, err = oracle.Latest(ctx)
	if err != nil {
		return nil, statusFromOracleErr(err)
	}
	if latest == nil || blocks.IsDummyHeader(latest) {
		return unchangedResponse(effective, oldest, tip), nil
	}
	tip = latest.Height
	oldest = advertisedOldest(oracle, tip)
	headers, last, err = catchUp(ctx, oracle, effective, tip, limit)
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		return headersResponse(headers, last, oldest, tip), nil
	}
	replaced, err := replacementAtCursor(ctx, oracle, effective, latest)
	if err != nil {
		return nil, err
	}
	if replaced != nil {
		return headersResponse([]*gen.BlockHeader{HeaderToProto(replaced)}, effective, oldest, tip), nil
	}
	return unchangedResponse(effective, oldest, tip), nil
}

// advertisedOldest is max(theoretical floor, lowest stored height) so a
// sparse/restart cache does not advertise a window At() cannot serve.
func advertisedOldest(oracle blocks.BlockOracle, tip int64) int64 {
	oldest := blocks.OldestHeight(tip)
	type storedOldest interface {
		StoredOldest() int64
	}
	s, ok := oracle.(storedOldest)
	if !ok {
		return oldest
	}
	if n := s.StoredOldest(); n > oldest {
		return n
	}
	return oldest
}

// replacementAtCursor returns the header at from_height when a waiter at
// tip was woken by a same-height hash change (catch-up of tip+1 is empty).
func replacementAtCursor(ctx context.Context, oracle blocks.BlockOracle, from int64, latest *blocks.Header) (*blocks.Header, error) {
	if latest == nil || latest.Height != from {
		return nil, nil
	}
	h, err := oracle.At(ctx, from)
	if err != nil {
		if errors.Is(err, blocks.ErrHeaderNotFound) {
			return nil, nil
		}
		return nil, statusFromOracleErr(err)
	}
	if h == nil || blocks.IsDummyHeader(h) {
		return nil, nil
	}
	return h, nil
}

func clampFromHeight(from, oldest int64) int64 {
	if oldest < 1 {
		oldest = 1
	}
	floorCursor := oldest - 1
	if floorCursor < 0 {
		floorCursor = 0
	}
	if from == 0 || from < floorCursor {
		return floorCursor
	}
	return from
}

func clampMaxHeaders(requested uint32) int {
	if requested == 0 || requested > uint32(blocks.MaxHeadersPerPoll) {
		return blocks.MaxHeadersPerPoll
	}
	return int(requested)
}

func catchUp(ctx context.Context, oracle blocks.BlockOracle, from, tip int64, limit int) ([]*gen.BlockHeader, int64, error) {
	start := from + 1
	if start <= 0 || start > tip || limit <= 0 {
		return nil, from, nil
	}
	end := start + int64(limit) - 1
	if end > tip {
		end = tip
	}
	out := make([]*gen.BlockHeader, 0, end-start+1)
	last := from
	for height := start; height <= end; height++ {
		h, err := oracle.At(ctx, height)
		if err != nil {
			if errors.Is(err, blocks.ErrHeaderNotFound) {
				break
			}
			return nil, last, statusFromOracleErr(err)
		}
		if h == nil || blocks.IsDummyHeader(h) {
			break
		}
		out = append(out, HeaderToProto(h))
		last = height
	}
	return out, last, nil
}

func headersResponse(headers []*gen.BlockHeader, last, oldest, tip int64) *gen.GetBlockHeadersResponse {
	return &gen.GetBlockHeadersResponse{
		Headers:        headers,
		NextFromHeight: last,
		OldestHeight:   oldest,
		TipHeight:      tip,
	}
}

func unchangedResponse(from, oldest, tip int64) *gen.GetBlockHeadersResponse {
	return &gen.GetBlockHeadersResponse{
		Unchanged:      true,
		NextFromHeight: from,
		OldestHeight:   oldest,
		TipHeight:      tip,
	}
}

func waitHeader(ctx context.Context, wake <-chan *blocks.Header, maxWait time.Duration) (bool, error) {
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case <-wake:
		return true, nil
	case <-timer.C:
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
