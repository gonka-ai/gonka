package user

import (
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"
)

// composeEmptyDiffs composes n diffs without contacting a host, so every
// cursor stays at 0 and only the cap trims s.diffs.
func composeEmptyDiffs(t *testing.T, session *Session, n int) map[uint64][]byte {
	t.Helper()
	roots := make(map[uint64][]byte, n)
	session.mu.Lock()
	defer session.mu.Unlock()
	for range n {
		diff, _, err := session.composeDiffLocked(nil)
		require.NoError(t, err)
		roots[diff.Nonce] = diff.PostStateRoot
	}
	return roots
}

func requireContiguous(t *testing.T, diffs []types.Diff, from, to uint64) {
	t.Helper()
	require.Len(t, diffs, int(to-from+1))
	for i, d := range diffs {
		require.Equal(t, from+uint64(i), d.Nonce)
	}
}

func TestSession_RetainedDiffsAreCappedWithAStore(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = maxRetainedDiffs + 40
	roots := composeEmptyDiffs(t, session, n)

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Len(t, session.diffs, maxRetainedDiffs, "a host at cursor 0 no longer keeps the whole journal in memory")
	require.Equal(t, uint64(n-maxRetainedDiffs+1), session.diffs[0].Nonce)

	requireContiguous(t, session.diffsForHost(0), 1, n)

	root, ok, err := session.postStateRootForNonce(1)
	require.NoError(t, err)
	require.True(t, ok, "a trimmed nonce is verified against the store")
	require.Equal(t, roots[1], root)
}

func TestSession_NoStoreKeepsTheDiffsACursorNeeds(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	const n = maxRetainedDiffs + 40
	composeEmptyDiffs(t, session, n)

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Len(t, session.diffs, n, "without a store there is nothing to read a trimmed diff from")
}

func TestSession_LateResponseForATrimmedNonceIsVerifiedFromTheStore(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	slow, err := session.PrepareInference(params)
	require.NoError(t, err)
	slowResp, err := session.SendOnly(ctx, slow, nil, nil)
	require.NoError(t, err)
	for range 3 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.mu.Lock()
	require.Greater(t, session.firstRetainedNonceLocked(), slowResp.Nonce,
		"precondition: every cursor passed the slow nonce, so it is trimmed")
	session.mu.Unlock()

	require.NoError(t, session.ProcessResponse(slow.hostIdx, slowResp, slow.diff.Nonce),
		"a stream that ends after its nonce is trimmed is still accepted")
}

func TestSession_LateResponseForATrimmedNonceWithAWrongRootIsRejected(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	slow, err := session.PrepareInference(params)
	require.NoError(t, err)
	slowResp, err := session.SendOnly(ctx, slow, nil, nil)
	require.NoError(t, err)
	for range 3 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.mu.Lock()
	require.Greater(t, session.firstRetainedNonceLocked(), slowResp.Nonce,
		"precondition: every cursor passed the slow nonce, so it is trimmed")
	session.mu.Unlock()

	forged := *slowResp
	forged.StateHash = append([]byte(nil), slowResp.StateHash...)
	require.NotEmpty(t, forged.StateHash)
	forged.StateHash[0] ^= 0xff
	err = session.ProcessResponse(slow.hostIdx, &forged, slow.diff.Nonce)
	require.ErrorIs(t, err, types.ErrStateHashMismatch,
		"the stored root of a trimmed nonce is compared exactly")
}

// failRootReadStore fails the one-nonce read postStateRootForNonce makes.
type failRootReadStore struct {
	storage.Storage
	nonce uint64
}

var errRootRead = errors.New("store unavailable")

func (s *failRootReadStore) GetDiffs(escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	if from == s.nonce && to == s.nonce {
		return nil, errRootRead
	}
	return s.Storage.GetDiffs(escrowID, from, to)
}

// A store that cannot produce the root says nothing about the host, so the
// response is refused as a local failure, not as a diverged host.
func TestSession_LateResponseWhenTheStoreFailsIsNotAMismatch(t *testing.T) {
	store := &failRootReadStore{Storage: storage.NewMemory()}
	session := setupStoredSession(t, store)
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}

	slow, err := session.PrepareInference(params)
	require.NoError(t, err)
	slowResp, err := session.SendOnly(ctx, slow, nil, nil)
	require.NoError(t, err)
	for range 3 {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	session.mu.Lock()
	require.Greater(t, session.firstRetainedNonceLocked(), slowResp.Nonce,
		"precondition: every cursor passed the slow nonce, so it is trimmed")
	cursorBefore := session.hostSyncNonce[slow.hostIdx]
	session.mu.Unlock()

	store.nonce = slowResp.Nonce
	err = session.ProcessResponse(slow.hostIdx, slowResp, slow.diff.Nonce)
	require.ErrorIs(t, err, ErrLocalRootUnavailable)
	require.ErrorIs(t, err, errRootRead)
	require.NotErrorIs(t, err, types.ErrStateHashMismatch)

	session.mu.Lock()
	require.Equal(t, cursorBefore, session.hostSyncNonce[slow.hostIdx], "an unverified response does not move the cursor")
	session.mu.Unlock()
}

func TestSession_RewindWithAStoreReachesTheStartOfTheJournal(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = maxRetainedDiffs + 40
	composeEmptyDiffs(t, session, n)
	session.mu.Lock()
	session.hostSyncNonce[2] = n
	session.mu.Unlock()

	require.True(t, session.RewindHostCatchUp(2, "host lost the escrow"))

	session.mu.Lock()
	defer session.mu.Unlock()
	require.Zero(t, session.hostSyncNonce[2], "the store holds the journal from nonce 1")
	requireContiguous(t, session.diffsForHost(2), 1, n)
}

type recordingCatchUpClient struct {
	*InProcessClient
	mu     sync.Mutex
	chunks [][2]uint64
}

func (c *recordingCatchUpClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	c.mu.Lock()
	c.chunks = append(c.chunks, [2]uint64{req.Diffs[0].Nonce, req.Diffs[len(req.Diffs)-1].Nonce})
	c.mu.Unlock()
	return c.InProcessClient.Send(ctx, req, stream, onReceipt)
}

func TestSession_SendCatchUpReadsEachChunkFromTheStore(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = maxRetainedDiffs + 40
	composeEmptyDiffs(t, session, n)
	client := &recordingCatchUpClient{InProcessClient: session.clients[0].(*InProcessClient)}

	require.NoError(t, session.sendCatchUpWith(context.Background(), 0, client))

	require.Equal(t, [][2]uint64{{1, catchUpChunkSize}, {catchUpChunkSize + 1, n}}, client.chunks)
	session.mu.Lock()
	defer session.mu.Unlock()
	require.Equal(t, uint64(n), session.hostSyncNonce[0])
}

type recordedSend struct {
	from, to, nonce uint64
	payload         bool
}

// recordingSendClient records the diff range, request nonce, and payload
// presence of every request.
type recordingSendClient struct {
	*InProcessClient
	mu    sync.Mutex
	sends []recordedSend
}

func (c *recordingSendClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, onReceipt func(*host.HostResponse)) (*host.HostResponse, error) {
	rec := recordedSend{nonce: req.Nonce, payload: req.Payload != nil}
	if len(req.Diffs) > 0 {
		rec.from, rec.to = req.Diffs[0].Nonce, req.Diffs[len(req.Diffs)-1].Nonce
	}
	c.mu.Lock()
	c.sends = append(c.sends, rec)
	c.mu.Unlock()
	return c.InProcessClient.Send(ctx, req, stream, onReceipt)
}

func TestSession_InlineCatchUpIsAtMostOneChunk(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = catchUpChunkSize + 50
	composeEmptyDiffs(t, session, n)

	session.mu.Lock()
	defer session.mu.Unlock()
	_, ok := session.inlineCatchUpLocked(0, n)
	require.False(t, ok, "a host at 0 is more than one chunk behind the tip")

	diffs, ok := session.inlineCatchUpLocked(0, catchUpChunkSize)
	require.True(t, ok)
	requireContiguous(t, diffs, 1, catchUpChunkSize)

	session.hostSyncNonce[0] = 60
	diffs, ok = session.inlineCatchUpLocked(0, n)
	require.True(t, ok)
	requireContiguous(t, diffs, 61, n)
}

func TestSession_InferenceToAFarBehindHostCatchesUpInChunksFirst(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	const n = catchUpChunkSize + 100
	composeEmptyDiffs(t, session, n)
	recorders := make([]*recordingSendClient, len(session.clients))
	for i, c := range session.clients {
		recorders[i] = &recordingSendClient{InProcessClient: c.(*InProcessClient)}
		session.clients[i] = recorders[i]
	}

	resp, err := session.SendInference(context.Background(), InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(n+1), resp.Nonce)

	var sends []recordedSend
	for _, r := range recorders {
		sends = append(sends, r.sends...)
	}
	require.Equal(t, []recordedSend{
		{from: 1, to: catchUpChunkSize, nonce: catchUpChunkSize},
		{from: catchUpChunkSize + 1, to: n, nonce: n},
		{from: n + 1, to: n + 1, nonce: n + 1, payload: true},
	}, sends, "the earlier nonces go in chunks, and the inference carries only its own diff")
}

// hostSlot returns the first slot owned by host i.
func hostSlot(session *Session, i int) uint32 {
	return session.addrToSlots[session.group[i].ValidatorAddress][0]
}

func TestSession_SignaturesAreTrimmedBelowEveryCursor(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	const sends = 12
	for range sends {
		_, err := session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	floor := min(minHostSyncNonce(session.hostSyncNonce, len(session.group)), session.nonce-1)
	require.Greater(t, floor, uint64(len(session.group)), "precondition: every cursor moved past the first round")
	var below int
	for nonce := range session.signatures {
		if nonce <= floor {
			below++
		}
	}
	require.LessOrEqual(t, below, len(session.group),
		"below the cursors only each validator's highest signature stays")
	require.NotContains(t, session.signatures, uint64(1))

	stored, err := session.store.GetSignatures(session.escrowID, 1)
	require.NoError(t, err)
	require.NotEmpty(t, stored, "precondition: nonce 1 was signed and persisted")
	for slot := range stored {
		require.True(t, session.hostSignedLocked(1, session.sm.SlotAddress(slot)),
			"a trimmed nonce is read back from the store")
	}
}

func TestSession_TrimKeepsTheCurrentNonceAndTheQuorumStatus(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	composeEmptyDiffs(t, session, 6)
	a, b, c := hostSlot(session, 0), hostSlot(session, 1), hostSlot(session, 2)
	sig := []byte("sig")

	session.mu.Lock()
	defer session.mu.Unlock()
	session.signatures = map[uint64]map[uint32][]byte{
		1: {a: sig, b: sig},
		2: {b: sig},
		3: {a: sig},
		6: {c: sig},
	}
	wantEntries, wantHighest, wantAny := session.signatureStatusLocked()

	for i := range session.group {
		session.hostSyncNonce[i] = 6
	}
	session.dropDiffPrefixLocked()

	require.ElementsMatch(t, []uint64{2, 3, 6}, slices.Collect(maps.Keys(session.signatures)),
		"nonce 1 is no validator's highest; 2 and 3 are, and 6 is current")
	_, gotHighest, gotAny := session.signatureStatusLocked()
	require.Equal(t, wantAny, gotAny)
	require.Equal(t, wantHighest, gotHighest, "the trim does not change the quorum nonce")
	require.NotEmpty(t, wantEntries)
	require.Equal(t, uint64(5), session.sigsTrimmedThrough, "the current nonce is never trimmed")
}

func TestSession_QuorumAtATrimmedNonceIsReadFromTheStore(t *testing.T) {
	session := setupStoredSession(t, storage.NewMemory())
	composeEmptyDiffs(t, session, 6)
	for i := range session.group {
		for _, slot := range session.addrToSlots[session.group[i].ValidatorAddress] {
			require.NoError(t, session.store.AddSignature(session.escrowID, 2, slot, []byte("sig")))
		}
	}

	session.mu.Lock()
	for i := range session.group {
		session.hostSyncNonce[i] = 6
	}
	session.dropDiffPrefixLocked()
	require.NotContains(t, session.signatures, uint64(2))
	session.mu.Unlock()

	require.True(t, session.hasQuorum(2, session.sm.QuorumThreshold()),
		"a quorum check below the trim floor reads the stored signatures")
	require.False(t, session.hasQuorum(3, session.sm.QuorumThreshold()))
}
