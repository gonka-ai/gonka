package scenarios

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/registry"
	"devshard/heightsync"
	"devshard/host"
	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
	"devshard/user"
)

var (
	errParticipantOffline = errors.New("participant: offline")
	errReasonNotModelled  = errors.New("not modelled by the fleet")
	errParticipantStalled = errors.New("participant: stalled until the fleet is released")
	errPromptOverContext  = errors.New("participant: prompt over the host's context length")
	errGatewayExited      = errors.New("fleet: the gateway process that owned this session exited")
)

const (
	executionTimeoutSeconds = 1920
	bytesPerToken           = 4
)

// participantBehaviour is what one participant does, read at the moment it is asked; only the validator's verdict is fixed when an escrow is built.
type participantBehaviour struct {
	delay        time.Duration
	stall        bool
	offline      bool
	votesOffline bool
	votesInvalid bool
	inputTokens  uint64
	contextLimit uint64
	unlisted     bool
}

// fleetEscrow keeps the user side in its own storage, so every session recovers from it as production does.
type fleetEscrow struct {
	group       []types.SlotAssignment
	store       *storage.Memory
	machine     *state.StateMachine
	open        bool
	exited      *atomic.Bool
	stopCadence func()
	hosts       []*host.Host
	clients     []user.HostClient
	diffs       diffLog
}

// indexedEscrowSession is what the registry's own session handle offers: the session and the record reads the open-record index uses.
type indexedEscrowSession interface {
	registry.EscrowSession
	registry.OpenRecordReader
}

// fleetSessionHandle stops the escrow's cadence and marks its session closed when the gateway closes it.
type fleetSessionHandle struct {
	indexedEscrowSession
	closed      func()
	stopCadence func()
}

func (handle fleetSessionHandle) Close() error {
	defer handle.closed()
	handle.stopCadence()
	return handle.indexedEscrowSession.Close()
}

// diffLog keeps every diff the session has sent, once per nonce and in nonce order.
type diffLog struct {
	mu      sync.Mutex
	entries []types.Diff
}

func (log *diffLog) record(diffs []types.Diff) {
	log.mu.Lock()
	defer log.mu.Unlock()
	for _, diff := range diffs {
		position, found := slices.BinarySearchFunc(log.entries, diff.Nonce, func(entry types.Diff, nonce uint64) int {
			switch {
			case entry.Nonce < nonce:
				return -1
			case entry.Nonce > nonce:
				return 1
			}
			return 0
		})
		if !found {
			log.entries = slices.Insert(log.entries, position, diff)
		}
	}
}

func (log *diffLog) upTo(latestNonce uint64) []types.Diff {
	log.mu.Lock()
	defer log.mu.Unlock()
	var stored []types.Diff
	for _, entry := range log.entries {
		if entry.Nonce <= latestNonce {
			stored = append(stored, entry)
		}
	}
	return stored
}

// hostFleet serves every escrow the fake chain holds from real hosts, one per participant slot.
type hostFleet struct {
	t              *testing.T
	chain          *fakeChain
	creator        *signing.Secp256k1Signer
	signers        map[string]*signing.Secp256k1Signer
	order          []string
	validationRate uint32

	behavioursMu sync.Mutex
	behaviours   map[string]*participantBehaviour

	escrowsMu sync.Mutex
	escrows   map[string]*fleetEscrow

	released    chan struct{}
	releaseOnce sync.Once

	sendsMu sync.Mutex
	sends   map[string]int
}

func newHostFleet(t *testing.T, blockchain *fakeChain, creator *signing.Secp256k1Signer, participants []*signing.Secp256k1Signer, validationRate uint32) *hostFleet {
	fleet := &hostFleet{
		t: t, chain: blockchain, creator: creator, validationRate: validationRate,
		signers:    map[string]*signing.Secp256k1Signer{},
		behaviours: map[string]*participantBehaviour{},
		escrows:    map[string]*fleetEscrow{},
		released:   make(chan struct{}),
		sends:      map[string]int{},
	}
	for _, participant := range participants {
		fleet.signers[participant.Address()] = participant
		fleet.order = append(fleet.order, participant.Address())
		fleet.behaviours[participant.Address()] = &participantBehaviour{}
	}
	return fleet
}

func formatEscrowID(escrowID uint64) string { return strconv.FormatUint(escrowID, 10) }

func (f *hostFleet) behaviourOf(address string) participantBehaviour {
	f.behavioursMu.Lock()
	defer f.behavioursMu.Unlock()
	return *f.behaviours[address]
}

func (f *hostFleet) changeBehaviour(indexes []int, change func(*participantBehaviour)) {
	f.behavioursMu.Lock()
	defer f.behavioursMu.Unlock()
	if indexes == nil {
		for _, address := range f.order {
			change(f.behaviours[address])
		}
		return
	}
	for _, index := range indexes {
		change(f.behaviours[f.order[index]])
	}
}

func (f *hostFleet) release() { f.releaseOnce.Do(func() { close(f.released) }) }

// serving opens a session as servingSessions does, with a drain cadence standing in for production's heartbeat.
func (f *hostFleet) serving(_ context.Context, escrowID string) (registry.EscrowSession, error) {
	return f.open(escrowID, true)
}

// readOnly opens a session as readOnlySessions does, with no cadence.
func (f *hostFleet) readOnly(_ context.Context, escrowID string) (registry.EscrowSession, error) {
	return f.open(escrowID, false)
}

// open recovers the session from the escrow's storage and refuses to open a second one over a session still open.
func (f *hostFleet) open(escrowID string, withCadence bool) (registry.EscrowSession, error) {
	built, err := f.escrowFor(escrowID)
	if err != nil {
		return nil, err
	}
	f.escrowsMu.Lock()
	defer f.escrowsMu.Unlock()
	if built.open {
		return nil, fmt.Errorf("session for %s requested while one is still open", escrowID)
	}
	exited := &atomic.Bool{}
	clients := make([]user.HostClient, 0, len(built.clients))
	for _, client := range built.clients {
		sessionClient := *client.(*fleetClient)
		if f.behaviourOf(sessionClient.address).unlisted {
			clients = append(clients, unlistedClient{address: sessionClient.address})
			continue
		}
		sessionClient.exited = exited
		clients = append(clients, &sessionClient)
	}
	session, machine, err := user.RecoverSession(built.store, f.creator, signing.NewSecp256k1Verifier(), escrowID, "", built.group, clients)
	if err != nil {
		return nil, fmt.Errorf("opening the fleet session of %s: %w", escrowID, err)
	}
	built.machine, built.open, built.exited = machine, true, exited
	var closeOnce sync.Once
	closed := func() {
		closeOnce.Do(func() {
			f.escrowsMu.Lock()
			defer f.escrowsMu.Unlock()
			if built.exited == exited {
				built.open = false
			}
		})
	}
	stopCadence := func() {}
	if withCadence {
		stopCadence = f.startDrainCadence(escrowID, session, machine)
	}
	built.stopCadence = stopCadence
	handle, indexed := registry.NewSessionHandle(session, machine).(indexedEscrowSession)
	if !indexed {
		stopCadence()
		built.open = false
		return nil, fmt.Errorf("the session handle of %s offers no record reads", escrowID)
	}
	return fleetSessionHandle{indexedEscrowSession: handle, closed: closed, stopCadence: stopCadence}, nil
}

// startDrainCadence models composeHeartbeatSpan's drain of queued host txs, which production runs only with height sync on and gated on turn state; the harness sends them every 24 s in one diff.
func (f *hostFleet) startDrainCadence(escrowID string, session *user.Session, machine *state.StateMachine) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(heightsync.DefaultHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-f.released:
				return
			case <-ticker.C:
				if machine.Phase() != types.PhaseActive || len(session.PendingTxs()) == 0 {
					continue
				}
				if err := session.SendPendingDiff(ctx); err != nil {
					f.t.Logf("drain cadence of %s: %v", escrowID, err)
				}
			}
		}
	}()
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			cancel()
			<-done
		})
	}
}

// severOpenSessions models the exit of the gateway process that owns every session still open: its hosts stay, its calls fail, and a new session may open.
func (f *hostFleet) severOpenSessions() int {
	f.escrowsMu.Lock()
	var cadences []func()
	for _, built := range f.escrows {
		if !built.open {
			continue
		}
		built.exited.Store(true)
		built.open = false
		cadences = append(cadences, built.stopCadence)
	}
	f.escrowsMu.Unlock()
	for _, stopCadence := range cadences {
		stopCadence()
	}
	return len(cadences)
}

func (f *hostFleet) userMachine(escrowID string) (*state.StateMachine, bool) {
	f.escrowsMu.Lock()
	defer f.escrowsMu.Unlock()
	built, known := f.escrows[escrowID]
	if !known || built.machine == nil {
		return nil, false
	}
	return built.machine, true
}

func (f *hostFleet) builtEscrowIDs() []string {
	f.escrowsMu.Lock()
	defer f.escrowsMu.Unlock()
	ids := make([]string, 0, len(f.escrows))
	for escrowID := range f.escrows {
		ids = append(ids, escrowID)
	}
	return ids
}

func (f *hostFleet) escrowFor(escrowID string) (*fleetEscrow, error) {
	f.escrowsMu.Lock()
	defer f.escrowsMu.Unlock()
	if built, known := f.escrows[escrowID]; known {
		return built, nil
	}
	numericID, err := strconv.ParseUint(escrowID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("escrow id %q is not numeric: %w", escrowID, err)
	}
	record, found := f.chain.escrowRecord(numericID)
	if !found {
		return nil, fmt.Errorf("escrow %s is not on the fake chain", escrowID)
	}
	signers := make([]*signing.Secp256k1Signer, len(record.slots))
	for index, address := range record.slots {
		signers[index] = f.signers[address]
	}
	group := testutil.MakeGroup(signers)
	configuration := f.sessionConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	built := &fleetEscrow{group: group, store: testutil.MustMemoryStore(f.t, escrowID, f.creator.Address(), configuration, group, record.amount)}
	for _, signer := range signers {
		hostMachine := statetest.MustStateMachine(f.t, escrowID, configuration, group, record.amount, f.creator.Address(), verifier)
		var validator devshardpkg.ValidationEngine = stub.NewValidationEngine()
		if behaviour := f.behaviourOf(signer.Address()); behaviour.votesInvalid {
			validator = invalidVerdicts{}
		}
		options := []host.HostOption{host.WithGrace(100), host.WithValidator(validator)}
		hostNode, err := host.NewHost(hostMachine, signer, participantEngine{fleet: f, address: signer.Address()}, escrowID, group, nil, options...)
		if err != nil {
			return nil, fmt.Errorf("building host %s of %s: %w", signer.Address(), escrowID, err)
		}
		f.t.Cleanup(hostNode.Close)
		hostNode.Start()
		built.hosts = append(built.hosts, hostNode)
	}
	for index, hostNode := range built.hosts {
		built.clients = append(built.clients, &fleetClient{
			inner: &user.InProcessClient{Host: hostNode}, fleet: f, address: signers[index].Address(), escrow: built,
		})
	}
	f.escrows[escrowID] = built
	return built, nil
}

func (f *hostFleet) sessionConfig(groupSize int) types.SessionConfig {
	params, _, _ := f.chain.EscrowParams(context.Background())
	configuration := testutil.DefaultConfig(groupSize)
	configuration.TokenPrice = params.TokenPrice
	configuration.FeePerNonce = params.FeePerNonce
	configuration.CreateDevshardFee = params.CreateDevshardFee
	configuration.ExecutionTimeout = executionTimeoutSeconds
	if f.validationRate > 0 {
		configuration.ValidationRate = f.validationRate
	}
	return types.NormalizeSessionConfig(configuration, groupSize)
}

// participantEngine answers the way its participant currently behaves.
type participantEngine struct {
	fleet   *hostFleet
	address string
}

func (e participantEngine) Execute(ctx context.Context, request devshardpkg.ExecuteRequest) (*devshardpkg.ExecuteResult, error) {
	behaviour := e.fleet.behaviourOf(e.address)
	if behaviour.stall {
		<-e.fleet.released
		return nil, errParticipantStalled
	}
	if behaviour.delay > 0 {
		timer := time.NewTimer(behaviour.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if inputTokens := uint64(len(request.Prompt)) / bytesPerToken; behaviour.contextLimit > 0 && inputTokens+request.MaxTokens > behaviour.contextLimit {
		writeContextRefusal(request, behaviour.contextLimit, inputTokens)
		return nil, errPromptOverContext
	}
	answer := stub.NewInferenceEngine()
	if behaviour.inputTokens > 0 {
		answer.InputTokens = behaviour.inputTokens
	}
	return answer.Execute(ctx, request)
}

// writeContextRefusal relays vLLM's context-length 400 the way cmd/devshardd/inference/execute.go:101-110 does: one data event with the id overwritten, then [DONE].
func writeContextRefusal(request devshardpkg.ExecuteRequest, contextLimit, inputTokens uint64) {
	if request.ResponseWriter == nil {
		return
	}
	message := fmt.Sprintf("This model's maximum context length is %d tokens. However, you requested %d output tokens and your prompt contains %d input tokens, for a total of %d tokens. Please reduce the length of the input prompt or the number of requested output tokens.",
		contextLimit, request.MaxTokens, inputTokens, inputTokens+request.MaxTokens)
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "BadRequestError", "param": "input_tokens", "code": http.StatusBadRequest},
		"id":    fmt.Sprintf("devshard-%s-%d", request.EscrowID, request.InferenceID),
	})
	if err != nil {
		return
	}
	fmt.Fprintf(request.ResponseWriter, "data: %s\n\ndata: [DONE]\n\n", body)
}

// streamResponseWriter hands the attempt's stream to the engine as transport/server.go:505 does; it is no http.Flusher, so the stub engine's success path writes nothing to it.
type streamResponseWriter struct {
	stream io.Writer
	header http.Header
}

func (w *streamResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *streamResponseWriter) Write(chunk []byte) (int, error) { return w.stream.Write(chunk) }

func (w *streamResponseWriter) WriteHeader(int) {}

// invalidVerdicts votes every validation invalid.
type invalidVerdicts struct{}

func (invalidVerdicts) Validate(context.Context, devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	return &devshardpkg.ValidateResult{Valid: false, Reason: "participant votes invalid"}, nil
}

// fleetClient is one participant as the session sees it: offline when told, and a timeout verifier replaying transport/server.go in process.
type fleetClient struct {
	inner   *user.InProcessClient
	fleet   *hostFleet
	address string
	escrow  *fleetEscrow
	exited  *atomic.Bool
}

func (c *fleetClient) gatewayExited() bool { return c.exited != nil && c.exited.Load() }

func (c *fleetClient) Send(ctx context.Context, request host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if c.gatewayExited() {
		return nil, errGatewayExited
	}
	if c.fleet.behaviourOf(c.address).offline {
		return nil, errParticipantOffline
	}
	c.fleet.countSend(c.address)
	c.escrow.diffs.record(request.Diffs)
	guarded := &guardedWriter{target: stream}
	guardedReceipts := func(response *host.HostResponse) {
		if receiptHandler != nil && !guarded.isClosed() {
			receiptHandler(response)
		}
	}
	type sent struct {
		response *host.HostResponse
		err      error
	}
	done := make(chan sent, 1)
	go func() {
		response, err := c.sendInProcess(ctx, request, guarded, guardedReceipts)
		done <- sent{response: response, err: err}
	}()
	select {
	case result := <-done:
		return result.response, result.err
	case <-ctx.Done():
		guarded.close()
		return nil, ctx.Err()
	}
}

// sendInProcess is user.InProcessClient.Send with the stream handed to the engine, so an engine's own events reach the gateway as they do over HTTP.
func (c *fleetClient) sendInProcess(ctx context.Context, request host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	hostNode := c.inner.Host
	response, err := hostNode.HandleRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	if receiptHandler != nil {
		receiptHandler(response)
	}
	switch {
	case response.ExecutionJob != nil:
		response.ExecutionJob.ResponseWriter = &streamResponseWriter{stream: stream}
		if result, executeErr := hostNode.RunExecution(ctx, response.ExecutionJob); executeErr == nil && result != nil && len(result.ResponseBody) > 0 {
			writeStreamedAnswer(stream)
		}
		response.Mempool = hostNode.MempoolTxs()
	case len(response.CachedResponseBody) > 0:
		writeStreamedAnswer(stream)
	}
	return response, nil
}

// writeStreamedAnswer is the role marker, one content delta and [DONE], as user.InProcessClient writes them.
func writeStreamedAnswer(stream io.Writer) {
	fmt.Fprintf(stream, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	fmt.Fprintf(stream, "data: {\"choices\":[{\"delta\":{\"content\":\"stub\"}}]}\n\n")
	fmt.Fprintf(stream, "data: [DONE]\n\n")
}

func (c *fleetClient) GetSignatures(ctx context.Context, nonce uint64) (map[uint32][]byte, error) {
	if c.gatewayExited() {
		return nil, errGatewayExited
	}
	if c.fleet.behaviourOf(c.address).offline {
		return nil, errParticipantOffline
	}
	return c.inner.GetSignatures(ctx, nonce)
}

func (c *fleetClient) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, payload *host.InferencePayload, diffs []types.Diff, _ host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	if c.gatewayExited() {
		return false, nil, 0, nil, "", errGatewayExited
	}
	behaviour := c.fleet.behaviourOf(c.address)
	if behaviour.offline || behaviour.votesOffline {
		return false, nil, 0, nil, "", errParticipantOffline
	}
	verifier := c.inner.Host
	if len(diffs) > 0 {
		c.escrow.diffs.record(diffs)
		verifier.ApplyCatchUpDiffs(diffs)
	}
	snapshot, record, found := verifier.SnapshotForTimeout(inferenceID)
	if !found || record == nil {
		return false, nil, 0, nil, "", fmt.Errorf("inference %d not found", inferenceID)
	}
	mempool := verifier.MempoolTxs()
	executor := c.executorFor(inferenceID)
	nowUnix := time.Now().Unix()
	var accept bool
	var err error
	switch reason {
	case types.TimeoutReason_TIMEOUT_REASON_REFUSED:
		accept, err = host.VerifyRefusedTimeout(ctx, snapshot, inferenceID, record, payload, mempool, executor, verifier, verifier, snapshot.Config, nowUnix)
	case types.TimeoutReason_TIMEOUT_REASON_EXECUTION:
		accept, err = host.VerifyExecutionTimeout(ctx, snapshot, inferenceID, record, mempool, executor, verifier, verifier, snapshot.Config, nowUnix)
	default:
		return false, nil, 0, nil, "", errReasonNotModelled
	}
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	if !accept {
		return false, nil, 0, host.RecoveryTxsFor(verifier.MempoolTxs(), inferenceID), "", nil
	}
	content, err := types.CanonicalSignedBytes(&types.TimeoutVoteContent{
		EscrowId: verifier.EscrowID(), InferenceId: inferenceID, Reason: reason, Accept: true,
	})
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	signature, err := verifier.Signer().Sign(content)
	if err != nil {
		return false, nil, 0, nil, "", err
	}
	return true, signature, verifier.PrimarySlot(), nil, "", nil
}

func (c *fleetClient) VerifyErrorMiss(context.Context, uint64, []types.Diff, host.TimeoutArtifacts) (bool, []byte, uint32, []*types.DevshardTx, string, error) {
	return false, nil, 0, nil, "", errReasonNotModelled
}

func (c *fleetClient) executorFor(inferenceID uint64) host.ExecutorClient {
	executorIndex := int(inferenceID % uint64(len(c.escrow.hosts)))
	executorAddress := c.escrow.group[executorIndex].ValidatorAddress
	if c.fleet.behaviourOf(executorAddress).offline {
		return nil
	}
	return executorAdapter{executor: c.escrow.hosts[executorIndex], fleet: c.fleet}
}

// executorAdapter answers a verifier's questions to the executor in process, as transport/server.go:816-859 does over HTTP.
type executorAdapter struct {
	executor *host.Host
	fleet    *hostFleet
}

func (a executorAdapter) GetMempool(context.Context) ([]*types.DevshardTx, error) {
	return a.executor.MempoolTxs(), nil
}

func (a executorAdapter) ChallengeCatchUp(ctx context.Context, inferenceID uint64, diffs []types.DiffWire, _ string) error {
	done := make(chan error, 1)
	go func() {
		_, _, err := a.executor.ChallengeReceiptWire(ctx, inferenceID, nil, diffs, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-a.fleet.released:
		return errParticipantStalled
	}
}

func (a executorAdapter) ChallengeReceipt(ctx context.Context, inferenceID uint64, payload *host.InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	type challenged struct {
		receipt []byte
		err     error
	}
	done := make(chan challenged, 1)
	go func() {
		receipt, _, err := a.executor.ChallengeReceipt(ctx, inferenceID, payload, diffs)
		done <- challenged{receipt: receipt, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			return nil, nil, result.err
		}
		return result.receipt, host.RecoveryTxsFor(a.executor.MempoolTxs(), inferenceID), nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-a.fleet.released:
		return nil, nil, errParticipantStalled
	}
}

// guardedWriter drops writes once the caller of Send has given up, so a late stream never races the caller.
type guardedWriter struct {
	mu     sync.Mutex
	target io.Writer
	closed bool
}

func (w *guardedWriter) Write(chunk []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.target == nil {
		return len(chunk), nil
	}
	return w.target.Write(chunk)
}

func (w *guardedWriter) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *guardedWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
}

func testFleetSigners(t *testing.T, count int) []*signing.Secp256k1Signer {
	t.Helper()
	signers := make([]*signing.Secp256k1Signer, count)
	for index := range signers {
		signers[index] = testutil.MustGenerateKey(t)
	}
	return signers
}

func testFleetChain(participants []*signing.Secp256k1Signer, creator *signing.Secp256k1Signer) *fakeChain {
	addresses := make([]string, len(participants))
	for index, participant := range participants {
		addresses[index] = participant.Address()
	}
	shape := testFakeChain(inferenceEpoch()).shape
	shape.participants = addresses
	shape.wallets = map[string]uint64{creator.Address(): 10_000_000}
	return newFakeChain(shape)
}

// Test flow:
//  1. Build a fleet over a chain that holds no escrow 4242.
//  2. Assert asking the fleet to serve escrow 4242 returns an error rather than stopping the test.
func TestFleetRefusesAnEscrowTheChainDoesNotHold(t *testing.T) {
	participants, creator := testFleetSigners(t, 4), testutil.MustGenerateKey(t)
	fleet := newHostFleet(t, testFleetChain(participants, creator), creator, participants, 0)
	t.Cleanup(fleet.release)

	if _, err := fleet.serving(context.Background(), "4242"); err == nil {
		t.Fatalf("serving(4242) = nil error, want an error for an escrow the chain does not hold")
	}
}

// Test flow:
//  1. Fund escrow on the chain and make every participant stall after its receipt, inside a bubble.
//  2. Send a request through one fleet client with a context the caller cancels after one second.
//  3. Assert Send returns the cancellation within that second rather than blocking on the stalled host.
func TestFleetClientReturnsWhenTheCallerGivesUp(t *testing.T) {
	participants, creator := testFleetSigners(t, 4), testutil.MustGenerateKey(t)
	synctest.Test(t, func(t *testing.T) {
		blockchain := testFleetChain(participants, creator)
		escrowID := blockchain.fundEscrow(creator.Address(), "scenario-model", 1_000_000)
		fleet := newHostFleet(t, blockchain, creator, participants, 0)
		t.Cleanup(fleet.release)
		fleet.changeBehaviour(nil, func(behaviour *participantBehaviour) { behaviour.stall = true })
		built, err := fleet.escrowFor(formatEscrowID(escrowID))
		if err != nil {
			t.Fatalf("escrowFor() = %v, want nil", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		started := time.Now()

		diff := testutil.SignDiff(t, creator, formatEscrowID(escrowID), 1, []*types.DevshardTx{testutil.StartTx(1)})
		payload := &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}

		_, sendErr := built.clients[1].Send(ctx, host.HostRequest{Diffs: []types.Diff{diff}, Nonce: 1, Payload: payload}, nil, nil)

		if !errors.Is(sendErr, context.DeadlineExceeded) {
			t.Fatalf("Send() = %v, want context.DeadlineExceeded once the caller gives up", sendErr)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("Send() returned after %s, want at most 1s", elapsed)
		}
	})
}

// Test flow:
//  1. Fund an escrow on the chain and give every participant a one-token context, inside a bubble.
//  2. Send the executor a request through its fleet client, collecting the stream.
//  3. Assert the stream is vLLM's 400 relayed as devshardd relays it, one data event and [DONE], and that the gateway's parser reads the host's limit and the request's total from it.
func TestFleetHostOverItsContextRelaysVLLMsRefusal(t *testing.T) {
	participants, creator := testFleetSigners(t, 4), testutil.MustGenerateKey(t)
	synctest.Test(t, func(t *testing.T) {
		blockchain := testFleetChain(participants, creator)
		escrowID := blockchain.fundEscrow(creator.Address(), "scenario-model", 1_000_000)
		fleet := newHostFleet(t, blockchain, creator, participants, 0)
		t.Cleanup(fleet.release)
		fleet.changeBehaviour(nil, func(behaviour *participantBehaviour) { behaviour.contextLimit = 1 })
		built, err := fleet.escrowFor(formatEscrowID(escrowID))
		if err != nil {
			t.Fatalf("escrowFor() = %v, want nil", err)
		}
		diff := testutil.SignDiff(t, creator, formatEscrowID(escrowID), 1, []*types.DevshardTx{testutil.StartTx(1)})
		payload := &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
		var stream bytes.Buffer

		_, sendErr := built.clients[1].Send(t.Context(), host.HostRequest{Diffs: []types.Diff{diff}, Nonce: 1, Payload: payload}, &stream, nil)

		events := strings.Split(strings.TrimSuffix(stream.String(), "\n\n"), "\n\n")
		if sendErr != nil || len(events) != 2 || events[1] != "data: [DONE]" {
			t.Fatalf("Send() = %v with stream %q, want nil and one data event before [DONE]", sendErr, stream.String())
		}
		var refusal struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(events[0], "data: ")), &refusal); err != nil || refusal.Error.Code != http.StatusBadRequest {
			t.Fatalf("json.Unmarshal(%q) = %v with code %d, want nil and 400", events[0], err, refusal.Error.Code)
		}
		wantRequested := uint64(len(testutil.TestPrompt))/bytesPerToken + testutil.TestMaxTokens
		if contextLimit, contextRequested := filters.CapabilityLimits(refusal.Error.Message); contextLimit != 1 || contextRequested != wantRequested {
			t.Fatalf("CapabilityLimits(%q) = (%d, %d), want (1, %d)", refusal.Error.Message, contextLimit, contextRequested, wantRequested)
		}
	})
}

// Test flow:
//  1. Open a serving session on a funded escrow inside a bubble, then sever every open session as a gateway exit would.
//  2. Assert one session was severed and a new serving session opens over it.
//  3. Assert the severed session can no longer reach its hosts: its finalize fails.
func TestFleetSeveredSessionLosesItsHostsAndMakesRoomForANewOne(t *testing.T) {
	participants, creator := testFleetSigners(t, 4), testutil.MustGenerateKey(t)
	synctest.Test(t, func(t *testing.T) {
		blockchain := testFleetChain(participants, creator)
		escrowID := formatEscrowID(blockchain.fundEscrow(creator.Address(), "scenario-model", 1_000_000))
		fleet := newHostFleet(t, blockchain, creator, participants, 0)
		t.Cleanup(fleet.release)
		severed, err := fleet.serving(t.Context(), escrowID)
		if err != nil {
			t.Fatalf("serving(%s) = %v, want nil", escrowID, err)
		}

		severedCount := fleet.severOpenSessions()
		reopened, reopenErr := fleet.serving(t.Context(), escrowID)

		if severedCount != 1 || reopenErr != nil {
			t.Fatalf("severOpenSessions() = %d, serving again = %v, want 1 and nil", severedCount, reopenErr)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		withSession, exposed := severed.(interface{ UserSession() *user.Session })
		if !exposed {
			t.Fatalf("the severed handle %T exposes no user session", severed)
		}
		if err := withSession.UserSession().Finalize(t.Context()); err == nil {
			t.Fatalf("Finalize() on the severed session = nil, want an error: its hosts are gone with the process")
		}
	})
}

// Test flow:
//  1. Record diffs out of order with a repeated nonce in a diff log.
//  2. Ask the log for the diffs up to nonce 2.
//  3. Assert the answer holds nonces 1 and 2 once each, in order.
func TestFleetDiffLogKeepsNonceOrderAndDropsRepeats(t *testing.T) {
	var log diffLog
	log.record([]types.Diff{{Nonce: 3}, {Nonce: 1}})
	log.record([]types.Diff{{Nonce: 2}, {Nonce: 1}})

	stored := log.upTo(2)

	if len(stored) != 2 || stored[0].Nonce != 1 || stored[1].Nonce != 2 {
		t.Fatalf("upTo(2) = %v, want nonces [1 2]", stored)
	}
}

func (f *hostFleet) GetHostInfo(address string) (*bridge.HostInfo, error) {
	if f.behaviourOf(address).unlisted {
		return nil, fmt.Errorf("participant %s: %w", address, bridge.ErrParticipantNotFound)
	}
	return &bridge.HostInfo{URL: "fleet://" + address}, nil
}

func (f *hostFleet) countSend(address string) {
	f.sendsMu.Lock()
	defer f.sendsMu.Unlock()
	f.sends[address]++
}

func (f *hostFleet) sendsTo(address string) int {
	f.sendsMu.Lock()
	defer f.sendsMu.Unlock()
	return f.sends[address]
}

type unlistedClient struct{ address string }

func (c unlistedClient) Send(context.Context, host.HostRequest, io.Writer, func(*host.HostResponse)) (*host.HostResponse, error) {
	return nil, fmt.Errorf("host %s: %w", c.address, user.ErrHostAbsentFromChain)
}

// inferencesStartedOn counts the attempts started on an escrow, read from the diffs its hosts were sent; drain diffs start none.
func (f *hostFleet) inferencesStartedOn(escrowID string) int {
	f.escrowsMu.Lock()
	built, known := f.escrows[escrowID]
	f.escrowsMu.Unlock()
	if !known {
		return 0
	}
	started := 0
	for _, diff := range built.diffs.upTo(math.MaxUint64) {
		for _, transaction := range diff.Txs {
			if transaction.GetStartInference() != nil {
				started++
			}
		}
	}
	return started
}
