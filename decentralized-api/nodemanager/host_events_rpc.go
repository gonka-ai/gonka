package nodemanager

import (
	"context"
	"errors"
	"math"
	"time"

	"common/logging"
	"common/nodemanager/gen"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/longpoll"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hostEventStallBlocks is how far emitted_height may sit behind the node tip
// before a client that would wait is told the feed is unavailable.
const hostEventStallBlocks = 3

// GetHostEvents returns subscribed events after the client's cursor.
//
// Where the read starts:
//   - cursor > 0: after cursor. after_height is ignored, so a dispatch retry of seq-1 still returns that event.
//   - cursor = 0, after_height = X > 0, emitted_height = 0: Unavailable. Nothing has been committed, so X is not caught up. next_cursor is unset and the client retries this request.
//   - cursor = 0, after_height = X > 0, X < emitted_height, BLOCK(X) stored: after that block's sequence.
//   - cursor = 0, after_height = X > 0, X < emitted_height, no BLOCK(X), X+1 is this generation's first height: at the oldest retained event of this generation.
//   - cursor = 0, after_height = X > 0, X < emitted_height, no BLOCK(X): needs_reset, next_cursor = head, no events.
//   - cursor = 0, after_height = X = emitted_height, emitted_height > 0, BLOCK(X) stored: after that block's sequence, not head, so a height already in the ring is not skipped.
//   - cursor = 0, after_height = X > emitted_height, emitted_height > 0: at head. The client is ahead of this feed.
//   - cursor = 0, after_height = 0: at the oldest retained event.
//
// Start is resolved once per request. A wake or timeout re-read uses that sequence and ignores after_height.
//
// A page holds at most MaxHostEventsPage events. A full page sets next_cursor to the last returned sequence. A short page sets it to the head captured before the read. A start inside the ring is answered from the ring. A start older than the ring and still inside the store is answered from the store and does not wait. needs_reset carries no events, the current generation, and next_cursor = head. A long-poll that times out still at its cursor reports a stall as Unavailable. A client cancel or deadline on a store read returns that status. Any other store error is Unavailable.
//
// Until boot has restored the ring, head and generation come from the store. Pages use the store path up to that head. The ring's boot nonce is not returned. A client at that head gets Unavailable while the feed is stalled. A failed store head read returns Unavailable.
func (s *Server) GetHostEvents(ctx context.Context, req *gen.GetHostEventsRequest) (*gen.GetHostEventsResponse, error) {
	if s.hostEvents == nil {
		return nil, status.Error(codes.FailedPrecondition, "host events: ring not configured")
	}
	if len(req.GetSubscribe()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "host events: subscribe must be non-empty")
	}

	subscribe := hostEventKindsFromProto(req.GetSubscribe())
	maxWait := clampHostEventsMaxWait(req.GetMaxWaitSeconds())
	clientGen := req.GetGeneration()
	cursor := req.GetCursor()
	afterHeight := req.GetAfterHeight()

	for {
		resp, err, again, start := s.getHostEventsOnce(ctx, cursor, clientGen, afterHeight, subscribe, maxWait)
		if !again {
			return resp, err
		}
		cursor = start
		afterHeight = 0
	}
}

// getHostEventsOnce runs one subscribe → read → wait cycle. release is deferred
// so every exit path (return or loop-continue via again=true) deregisters the waiter.
func (s *Server) getHostEventsOnce(
	ctx context.Context,
	cursor, clientGen, afterHeight uint64,
	subscribe []apiconfig.HostEventKind,
	maxWait time.Duration,
) (resp *gen.GetHostEventsResponse, err error, again bool, start uint64) {
	var wake <-chan struct{}
	var release func()
	if maxWait > 0 {
		// Subscribe (with the client's kind filter) before the read to avoid
		// lost wake-ups. Only subscribed kinds wake this waiter, so an
		// unsubscribed kind (e.g. maintenance) cannot reset the deadline.
		wake, release = s.hostEvents.Subscribe(subscribe)
	}
	if release != nil {
		defer release()
	}
	escrow := subscribeIncludesEscrow(subscribe)

	got, resolved, readErr := s.collectHostEvents(ctx, cursor, clientGen, afterHeight, subscribe)
	start = resolved
	if readErr != nil {
		return nil, hostEventReadErr(readErr), false, start
	}
	if got.Reset || len(got.Events) > 0 || got.NextCursor > resolved {
		return s.finishHostEvents(got, escrow), nil, false, start
	}

	if maxWait <= 0 {
		return s.hostEventsResponse(got, nil, true, false, escrow), nil, false, start
	}
	if stalled, height := s.hostEventStall(); stalled {
		return nil, status.Errorf(codes.Unavailable, "host events: stalled at height %d", height), false, start
	}

	logging.Debug("host_events: GetHostEvents long-poll waiting", types.Config,
		"cursor", cursor,
		"nextCursor", got.NextCursor,
		"generation", got.Generation,
		"maxWait", maxWait,
	)
	outcome, waitErr := longpoll.Wait(ctx, wake, maxWait)
	if waitErr != nil {
		return nil, status.FromContextError(waitErr).Err(), false, start
	}
	if outcome == longpoll.TimedOut {
		// The re-read advances next_cursor past anything appended between
		// the timeout and now, so what it finds has to travel in this
		// response: returned as "unchanged" it would be skipped for good.
		got, resolved, readErr = s.collectHostEvents(ctx, start, clientGen, 0, subscribe)
		if readErr != nil {
			return nil, hostEventReadErr(readErr), false, start
		}
		logging.Debug("host_events: GetHostEvents long-poll timed out", types.Config,
			"cursor", cursor,
			"nextCursor", got.NextCursor,
			"generation", got.Generation,
			"maxWait", maxWait,
			"lateEvents", len(got.Events),
		)
		// A stall that starts during the wait does not append, so it does not
		// wake this client. An empty page still at the cursor it waited on is
		// the request that would have waited again.
		if got.Reset || len(got.Events) > 0 || got.NextCursor > resolved {
			return s.hostEventsTimedOutResponse(got, escrow), nil, false, start
		}
		if stalled, height := s.hostEventStall(); stalled {
			return nil, status.Errorf(codes.Unavailable, "host events: stalled at height %d", height), false, start
		}
		return s.hostEventsTimedOutResponse(got, escrow), nil, false, start
	}
	return nil, nil, true, start
}

func (s *Server) finishHostEvents(got apiconfig.HostEventSince, escrow bool) *gen.GetHostEventsResponse {
	if got.Reset {
		logging.Info("host_events: GetHostEvents needs_reset", types.Config,
			"serverGeneration", got.Generation,
			"nextCursor", got.NextCursor,
		)
		return s.hostEventsResponse(got, nil, false, true, escrow)
	}
	logging.Debug("host_events: GetHostEvents returning events", types.Config,
		"count", len(got.Events),
		"nextCursor", got.NextCursor,
		"generation", got.Generation,
	)
	return s.hostEventsResponse(got, hostEventsToProto(got.Events), len(got.Events) == 0, false, escrow)
}

// collectHostEvents resolves the cursor and reads one page.
// resolved is the sequence the page was read after. The caller waits only when
// the page is empty and next_cursor is still resolved.
func (s *Server) collectHostEvents(
	ctx context.Context,
	cursor, clientGen, afterHeight uint64,
	subscribe []apiconfig.HostEventKind,
) (apiconfig.HostEventSince, uint64, error) {
	if !s.ringRestored() {
		return s.collectUnrestored(ctx, cursor, clientGen, afterHeight, subscribe)
	}
	head, oldest, generation, empty := s.hostEvents.Bounds()
	out := apiconfig.HostEventSince{NextCursor: head, Generation: generation}
	if clientGen != 0 && clientGen != generation {
		out.Reset = true
		return out, cursor, nil
	}

	start, reset, storeGen, err := s.hostEventStart(ctx, cursor, afterHeight, head)
	if err != nil {
		return out, start, err
	}
	if reset {
		if storeGen != 0 {
			return s.resetFromStore(ctx, storeGen, head), start, nil
		}
		out.Reset = true
		return out, start, nil
	}
	if start > head {
		out.Reset = true
		return out, start, nil
	}
	if s.hostLog != nil && start < head && !ringHas(start, oldest, empty) {
		return s.readHostEventStore(ctx, start, head, generation, subscribe)
	}
	got := s.hostEvents.SincePage(start, clientGen, subscribe, apiconfig.MaxHostEventsPage)
	if got.Reset && s.hostLog != nil && (clientGen == 0 || clientGen == got.Generation) {
		return s.readHostEventStore(ctx, start, got.NextCursor, got.Generation, subscribe)
	}
	return got, start, nil
}

// ringRestored is false while boot has not loaded the ring. With no status,
// the ring is the feed.
func (s *Server) ringRestored() bool {
	if s.hostStatus == nil {
		return true
	}
	return s.hostStatus.RingRestored()
}

// collectUnrestored answers from the store. The ring still holds this
// process's boot nonce, so its generation and head are not the feed's.
func (s *Server) collectUnrestored(
	ctx context.Context,
	cursor, clientGen, afterHeight uint64,
	subscribe []apiconfig.HostEventKind,
) (apiconfig.HostEventSince, uint64, error) {
	if s.hostLog == nil {
		return apiconfig.HostEventSince{}, 0, errors.New("ring not restored")
	}
	seq, generation, _, err := s.hostLog.FeedHead(ctx)
	if err != nil {
		return apiconfig.HostEventSince{}, 0, err
	}
	out := apiconfig.HostEventSince{NextCursor: seq, Generation: generation}
	if clientGen != 0 && clientGen != generation {
		out.Reset = true
		return out, cursor, nil
	}
	start, reset, storeGen, err := s.hostEventStart(ctx, cursor, afterHeight, seq)
	if err != nil {
		return out, start, err
	}
	if reset {
		if storeGen != 0 {
			return s.resetFromStore(ctx, storeGen, seq), start, nil
		}
		out.Reset = true
		return out, start, nil
	}
	if start > seq {
		out.Reset = true
		return out, start, nil
	}
	if start < seq {
		return s.readHostEventStore(ctx, start, seq, generation, subscribe)
	}
	return out, start, nil
}

// errHostEventsNoHeight is a cursor-0 request that names an after_height before
// the feed has committed one. Answering it as caught up would burn that height.
var errHostEventsNoHeight = errors.New("no height committed")

// hostEventStart applies the cursor and after_height rule.
// reset means needs_reset. A cursor above zero ignores after_height.
// storeGen is set when the store generation differs from the ring's.
func (s *Server) hostEventStart(ctx context.Context, cursor, afterHeight, head uint64) (start uint64, reset bool, storeGen uint64, err error) {
	if cursor > 0 {
		return cursor, false, 0, nil
	}
	if afterHeight == 0 || s.hostStatus == nil {
		return 0, false, 0, nil
	}
	emitted := s.hostStatus.EmittedHeight()
	if emitted == 0 {
		return 0, false, 0, errHostEventsNoHeight
	}
	if afterHeight > uint64(math.MaxInt64) || int64(afterHeight) > emitted {
		return head, false, 0, nil
	}
	if s.hostLog == nil {
		if s.afterHeightIsGenerationStart(ctx, afterHeight) {
			return 0, false, 0, nil
		}
		if int64(afterHeight) == emitted {
			return head, false, 0, nil
		}
		return 0, true, 0, nil
	}
	seq, found, gen, err := s.hostLog.ResolveBlock(ctx, int64(afterHeight))
	if err != nil {
		return 0, false, 0, err
	}
	if s.storeGenerationMismatch(gen) {
		return 0, true, gen, nil
	}
	if found {
		if seq > head {
			return head, false, 0, nil
		}
		return seq, false, 0, nil
	}
	if s.afterHeightIsGenerationStart(ctx, afterHeight) {
		return 0, false, 0, nil
	}
	if int64(afterHeight) == emitted {
		return head, false, 0, nil
	}
	return 0, true, 0, nil
}

// afterHeightIsGenerationStart reports after_height + 1 is this generation's
// first stored BLOCK, so the client is caught up at the previous height.
func (s *Server) afterHeightIsGenerationStart(ctx context.Context, afterHeight uint64) bool {
	first, ok, err := s.generationFirstHeight(ctx)
	return err == nil && ok && int64(afterHeight)+1 == first
}

func (s *Server) generationFirstHeight(ctx context.Context) (int64, bool, error) {
	if s.hostLog != nil {
		rows, gen, err := s.hostLog.Read(ctx, 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 1)
		if err != nil {
			return 0, false, err
		}
		if !s.storeGenerationMismatch(gen) && len(rows) > 0 && rows[0].Block != nil && rows[0].Block.Height > 0 {
			return rows[0].Block.Height, true, nil
		}
	}
	if s.hostEvents != nil && s.ringRestored() {
		page := s.hostEvents.SincePage(0, 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 1)
		if !page.Reset && len(page.Events) > 0 && page.Events[0].Block != nil && page.Events[0].Block.Height > 0 {
			return page.Events[0].Block.Height, true, nil
		}
	}
	return 0, false, nil
}

func (s *Server) storeGenerationMismatch(storeGen uint64) bool {
	if storeGen == 0 || s.hostEvents == nil || !s.ringRestored() {
		return false
	}
	return storeGen != s.hostEvents.Generation()
}

func (s *Server) resetFromStore(ctx context.Context, storeGen, fallbackHead uint64) apiconfig.HostEventSince {
	out := apiconfig.HostEventSince{NextCursor: fallbackHead, Generation: storeGen, Reset: true}
	if s.hostLog == nil {
		return out
	}
	seq, gen, _, err := s.hostLog.FeedHead(ctx)
	if err != nil {
		return out
	}
	out.NextCursor = seq
	if gen != 0 {
		out.Generation = gen
	}
	return out
}

func ringHas(start, oldest uint64, empty bool) bool {
	if empty {
		return false
	}
	if start == 0 {
		return oldest <= 1
	}
	return oldest <= start+1
}

func (s *Server) readHostEventStore(
	ctx context.Context,
	start, head, generation uint64,
	subscribe []apiconfig.HostEventKind,
) (apiconfig.HostEventSince, uint64, error) {
	out := apiconfig.HostEventSince{NextCursor: head, Generation: generation}
	oldest, storeEmpty, oldestGen, err := s.hostLog.Oldest(ctx)
	if err != nil {
		return out, start, err
	}
	if s.storeGenerationMismatch(oldestGen) {
		return s.resetFromStore(ctx, oldestGen, head), start, nil
	}
	if storeEmpty || (start > 0 && oldest > start+1) {
		out.Reset = true
		return out, start, nil
	}
	rows, readGen, err := s.hostLog.Read(ctx, start, subscribe, apiconfig.MaxHostEventsPage)
	if err != nil {
		return out, start, err
	}
	if s.storeGenerationMismatch(readGen) {
		return s.resetFromStore(ctx, readGen, head), start, nil
	}
	// A prune can run between the first Oldest and Read. Re-check so a
	// client at the old boundary is reset instead of skipping the gap.
	oldest, storeEmpty, oldestGen, err = s.hostLog.Oldest(ctx)
	if err != nil {
		return out, start, err
	}
	if s.storeGenerationMismatch(oldestGen) {
		return s.resetFromStore(ctx, oldestGen, head), start, nil
	}
	if storeEmpty || (start > 0 && oldest > start+1) {
		out.Reset = true
		return out, start, nil
	}
	end := len(rows)
	for i, ev := range rows {
		if ev.Seq > head {
			end = i
			break
		}
	}
	out.Events = rows[:end]
	if len(rows) >= apiconfig.MaxHostEventsPage && len(out.Events) >= apiconfig.MaxHostEventsPage {
		out.NextCursor = out.Events[len(out.Events)-1].Seq
	}
	return out, start, nil
}

// hostEventReadErr maps a collect failure. A feed that has not committed a
// height stays Unavailable with an empty response, so the client retries the
// same after_height. A client cancel or deadline is that status. Every other
// store error is Unavailable.
func hostEventReadErr(err error) error {
	if errors.Is(err, errHostEventsNoHeight) {
		return status.Error(codes.Unavailable, "host events: no height committed")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	logging.Warn("host_events: GetHostEvents store read", types.Config, "error", err)
	return status.Errorf(codes.Unavailable, "host events: store read: %s", err.Error())
}

// hostEventStall reports a feed that is stalled, or more than three blocks behind the tip.
// The height is the next height the feed has not committed.
func (s *Server) hostEventStall() (bool, int64) {
	if s.hostStatus == nil {
		return false, 0
	}
	emitted := s.hostStatus.EmittedHeight()
	height := emitted + 1
	if s.hostStatus.Stalled() {
		return true, height
	}
	tip := s.hostStatus.TipHeight()
	if tip > emitted+hostEventStallBlocks {
		return true, height
	}
	return false, 0
}

// hostEventsTimedOutResponse is the answer to a long-poll that timed out: the
// re-read's events, if any arrived between the deadline and the re-read,
// and "unchanged" only when it got none.
func (s *Server) hostEventsTimedOutResponse(got apiconfig.HostEventSince, escrow bool) *gen.GetHostEventsResponse {
	if got.Reset {
		return s.hostEventsResponse(got, nil, false, true, escrow)
	}
	return s.hostEventsResponse(got, hostEventsToProto(got.Events), len(got.Events) == 0, false, escrow)
}

func (s *Server) hostEventsResponse(got apiconfig.HostEventSince, events []*gen.HostEvent, unchanged, needsReset, escrow bool) *gen.GetHostEventsResponse {
	resp := &gen.GetHostEventsResponse{
		Unchanged:      unchanged,
		Events:         events,
		NextCursor:     got.NextCursor,
		Generation:     got.Generation,
		NeedsReset:     needsReset,
		SupportedKinds: supportedHostEventKinds(),
	}
	if escrow {
		resp.EscrowLoad = s.escrowLoadSnapshot()
	}
	return resp
}

func supportedHostEventKinds() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
		gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED,
		gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_CANCELED,
	}
}

func subscribeIncludesEscrow(kinds []apiconfig.HostEventKind) bool {
	for _, k := range kinds {
		if k == apiconfig.HostEventKindEscrowCreated || k == apiconfig.HostEventKindEscrowSettled {
			return true
		}
	}
	return false
}

func (s *Server) escrowLoadSnapshot() []*gen.EscrowLoad {
	if s.escrowLoad == nil {
		return nil
	}
	snap := s.escrowLoad.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	out := make([]*gen.EscrowLoad, len(snap))
	for i, e := range snap {
		out[i] = &gen.EscrowLoad{
			EscrowId:       e.EscrowID,
			RequestsPerMin: e.RequestsPerMin,
		}
	}
	return out
}

func clampHostEventsMaxWait(maxWaitSeconds uint32) time.Duration {
	if maxWaitSeconds > math.MaxInt32 {
		return hostEventsMaxWaitCap()
	}
	return longpoll.ClampMaxWait(int32(maxWaitSeconds), hostEventsMaxWaitCap())
}

func hostEventKindsFromProto(in []gen.HostEventKind) []apiconfig.HostEventKind {
	out := make([]apiconfig.HostEventKind, len(in))
	for i, k := range in {
		out[i] = apiconfig.HostEventKind(k)
	}
	return out
}

func hostEventsToProto(in []apiconfig.HostEvent) []*gen.HostEvent {
	out := make([]*gen.HostEvent, len(in))
	for i, ev := range in {
		out[i] = hostEventToProto(ev)
	}
	return out
}

func hostEventToProto(ev apiconfig.HostEvent) *gen.HostEvent {
	msg := &gen.HostEvent{
		Seq:            ev.Seq,
		Kind:           gen.HostEventKind(ev.Kind),
		ObservedAtUnix: ev.ObservedAtUnix,
	}
	if ev.Escrow != nil {
		msg.Escrow = &gen.EscrowPayload{
			EscrowId:    ev.Escrow.EscrowID,
			EpochIndex:  ev.Escrow.EpochIndex,
			ModelId:     ev.Escrow.ModelID,
			Creator:     ev.Escrow.Creator,
			Amount:      ev.Escrow.Amount,
			Settler:     ev.Escrow.Settler,
			TotalPayout: ev.Escrow.TotalPayout,
			Fees:        ev.Escrow.Fees,
			Remainder:   ev.Escrow.Remainder,
		}
	}
	if ev.Maintenance != nil {
		msg.Maintenance = &gen.MaintenancePayload{
			ReservationId:  ev.Maintenance.ReservationID,
			Participant:    ev.Maintenance.Participant,
			StartHeight:    ev.Maintenance.StartHeight,
			DurationBlocks: ev.Maintenance.DurationBlocks,
			Reason:         ev.Maintenance.Reason,
		}
	}
	if ev.WarmKey != nil {
		msg.WarmKey = &gen.WarmKeyPayload{
			Granter:       ev.WarmKey.Granter,
			Grantee:       ev.WarmKey.Grantee,
			GrantId:       ev.WarmKey.GrantID,
			Height:        ev.WarmKey.Height,
			ExpiresAtUnix: ev.WarmKey.ExpiresAtUnix,
			End:           ev.WarmKey.End,
		}
	}
	if ev.Block != nil {
		msg.Block = &gen.BlockPayload{
			Height:           ev.Block.Height,
			Hash:             ev.Block.Hash,
			HeaderTimeUnixMs: ev.Block.HeaderTimeUnixMs,
			EpochIndex:       ev.Block.EpochIndex,
			Header:           ev.Block.Header,
		}
	}
	return msg
}
