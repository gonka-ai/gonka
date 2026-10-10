package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/storage"
	"devshard/stub"
	"devshard/transport"
)

func newOwnerLookupManager(t *testing.T, escrowIDs ...string) (*HostManager, *storage.SQLite, *signing.Secp256k1Signer, *countingEscrowBridge) {
	t.Helper()
	store := newManagerTestStore(t)
	hosts := make([]*signing.Secp256k1Signer, 3)
	slots := make([]string, len(hosts))
	for i := range hosts {
		hosts[i] = mustGenerateKey(t)
		slots[i] = hosts[i].Address()
	}
	user := mustGenerateKey(t)
	found := make(map[string]*bridge.EscrowInfo, len(escrowIDs))
	for _, id := range escrowIDs {
		found[id] = &bridge.EscrowInfo{
			EscrowID: id, EpochID: 7, Amount: 100000,
			CreatorAddress: user.Address(), Slots: slots, TokenPrice: 1,
		}
	}
	br := &countingEscrowBridge{found: found}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hosts[0], stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	return mgr, store, user, br
}

func warmEscrowRow(t *testing.T, store storage.Storage, info *bridge.EscrowInfo) {
	t.Helper()
	urls := make(map[string]string, len(info.Slots))
	for _, slot := range info.Slots {
		urls[slot] = "http://localhost"
	}
	require.NoError(t, store.PutEscrowCache(storage.EscrowCacheInfo{
		EscrowID: info.EscrowID, EpochID: info.EpochID, Amount: info.Amount,
		CreatorAddress: info.CreatorAddress, Slots: info.Slots, TokenPrice: info.TokenPrice, SlotURLs: urls,
	}))
}

// fillLookupFloor spends n floor charges with one stranger per unknown id.
func fillLookupFloor(t *testing.T, mgr *HostManager, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := fetchBind(mgr, fmt.Sprintf("8800%d", i), fmt.Sprintf("gonka1stranger%d", i))
		require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	}
	_, err := fetchBind(mgr, "88009", "gonka1strangerlast")
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited, "precondition: floor is full")
}

func lookupFloorUsed(mgr *HostManager) int {
	mgr.escrowLookupMu.Lock()
	defer mgr.escrowLookupMu.Unlock()
	return countRecent(mgr.escrowLookupFloor, time.Now())
}

func TestSessionForOwner_WarmRowBindsWithFullFloor(t *testing.T) {
	overrideLookupLimits(t, 2, 2)
	const escrowID = "8810"
	mgr, store, user, br := newOwnerLookupManager(t, escrowID)
	warmEscrowRow(t, store, br.found[escrowID])
	fillLookupFloor(t, mgr, 2)
	callsBefore := br.calls.Load()

	srv, err := mgr.sessionForOwner(escrowID, user.Address())
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.Equal(t, callsBefore+1, br.calls.Load(), "owner bind still reads the escrow live once")
	require.Equal(t, 2, lookupFloorUsed(mgr), "owner bind of a warmed id takes no floor charge")
}

func TestSessionForOwner_WarmRowSkipsStaleNotFound(t *testing.T) {
	const escrowID = "8811"
	mgr, store, user, br := newOwnerLookupManager(t, escrowID)
	info := br.found[escrowID]
	delete(br.found, escrowID)
	_, err := fetchBind(mgr, escrowID, "gonka1early")
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	br.found[escrowID] = info
	warmEscrowRow(t, store, info)

	srv, err := mgr.sessionForOwner(escrowID, user.Address())
	require.NoError(t, err)
	require.NotNil(t, srv)
}

func TestSessionForOwner_WarmRowOtherCreatorRefusesWithoutQuery(t *testing.T) {
	const escrowID = "8812"
	mgr, store, _, br := newOwnerLookupManager(t, escrowID)
	warmEscrowRow(t, store, br.found[escrowID])

	srv, err := mgr.sessionForOwner(escrowID, "gonka1notthecreator")
	require.NoError(t, err)
	require.Nil(t, srv)
	require.Zero(t, br.calls.Load())
	require.Zero(t, lookupFloorUsed(mgr))
}

func TestSessionForOwner_KnownCreatorBindsWithFullFloor(t *testing.T) {
	overrideLookupLimits(t, 2, 2)
	const first, second = "8813", "8814"
	mgr, _, user, _ := newOwnerLookupManager(t, first, second)
	srv, err := mgr.sessionForOwner(first, user.Address())
	require.NoError(t, err)
	require.NotNil(t, srv)
	fillLookupFloor(t, mgr, 2)

	srv, err = mgr.sessionForOwner(second, user.Address())
	require.NoError(t, err, "no escrow_cache row: a known creator still skips the floor")
	require.NotNil(t, srv)
}

func TestSessionForOwner_UnknownCreatorNoRowStillLimited(t *testing.T) {
	overrideLookupLimits(t, 2, 2)
	const escrowID = "8815"
	mgr, _, user, _ := newOwnerLookupManager(t, escrowID)
	fillLookupFloor(t, mgr, 2)

	_, err := mgr.sessionForOwner(escrowID, user.Address())
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
}

func TestFetchEscrowForBind_KnownCreatorKeepsPerSignerLimit(t *testing.T) {
	overrideLookupLimits(t, 2, 100)
	mgr, _, user, _ := newOwnerLookupManager(t)
	mgr.noteKnownCreator(user.Address(), time.Now())

	_, err := fetchBind(mgr, "8820", user.Address())
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "8821", user.Address())
	require.ErrorIs(t, err, bridge.ErrEscrowNotFound)
	_, err = fetchBind(mgr, "8822", user.Address())
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited)
	require.Zero(t, lookupFloorUsed(mgr))
}

func TestFetchEscrowForBind_KnownCreatorRefundsChainUnavailable(t *testing.T) {
	overrideLookupLimits(t, 2, 100)
	inner := &countingEscrowBridge{mockBridge: mockBridge{getEscrowErr: bridge.ErrChainUnavailable}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(storage.NewMemory(), mustGenerateKey(t), stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, inner, nil, nil))
	const owner = "gonka1owner"
	mgr.noteKnownCreator(owner, time.Now())

	for i := 0; i < 4; i++ {
		_, err := fetchBind(mgr, fmt.Sprintf("883%d", i), owner)
		require.ErrorIs(t, err, bridge.ErrChainUnavailable)
	}
	_, err := fetchBind(mgr, "8840", "gonka1stranger")
	require.ErrorIs(t, err, bridge.ErrChainUnavailable)
	_, err = fetchBind(mgr, "8841", "gonka1stranger")
	require.ErrorIs(t, err, bridge.ErrChainUnavailable)
	_, err = fetchBind(mgr, "8842", "gonka1stranger")
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited, "a stranger is still charged while the chain is down")
}

// Recovery binds from the store with no fetchEscrowForBind, so only the
// install hook can mark the owner known after a restart.
func TestRecoverSessions_NotesOwnerAsKnownCreator(t *testing.T) {
	store := newManagerTestStore(t)
	group, user, hostSigner := populateStore(t, store, 2)
	addresses := make([]string, len(group))
	for i, s := range group {
		addresses[i] = s.ValidatorAddress
	}
	br := &mockBridge{escrow: &bridge.EscrowInfo{EscrowID: "1", Amount: 100000, CreatorAddress: user.Address(), Slots: addresses}}
	mgr := waitRecoveryRepairsOnCleanup(t, NewHostManager(store, hostSigner, stub.NewInferenceEngine(), stub.NewValidationEngine(), nil, testutil.RuntimeTestVersion, br, nil, nil))
	require.False(t, mgr.isKnownCreator(user.Address()))

	require.NoError(t, mgr.RecoverSessions())
	require.True(t, mgr.isKnownCreator(user.Address()))

	overrideLookupLimits(t, 2, 1)
	_, err := mgr.chargeEscrowLookup("gonka1stranger", time.Now())
	require.NoError(t, err)
	_, err = mgr.chargeEscrowLookup("gonka1stranger2", time.Now())
	require.ErrorIs(t, err, bridge.ErrEscrowLookupLimited, "precondition: floor is full")
	_, err = mgr.chargeEscrowLookup(user.Address(), time.Now())
	require.NoError(t, err, "a recovered owner skips the full floor")
}

func TestNoteKnownCreator_BoundedDropsOldest(t *testing.T) {
	mgr := &HostManager{}
	now := time.Now()
	for i := 0; i < maxKnownCreators; i++ {
		mgr.noteKnownCreator(fmt.Sprintf("gonka1c%d", i), now.Add(time.Duration(i)*time.Millisecond))
	}
	mgr.noteKnownCreator("gonka1new", now.Add(time.Hour))
	require.Len(t, mgr.knownCreators, maxKnownCreators)
	require.False(t, mgr.isKnownCreator("gonka1c0"))
	require.True(t, mgr.isKnownCreator("gonka1c1"))
	require.True(t, mgr.isKnownCreator("gonka1new"))
}

func TestOwnerChat_StrangerAttachFloodDoesNotRefuseOwner(t *testing.T) {
	overrideLookupLimits(t, 2, 2)
	const escrowID = "8860"
	mgr, store, user, br := newOwnerLookupManager(t, escrowID)
	warmEscrowRow(t, store, br.found[escrowID])
	rpc := newBindRPC(t, mgr)

	for i := 0; i < 2; i++ {
		_, err := rpc.attach(fmt.Sprintf("8870%d", i), mustGenerateKey(t))
		require.Equal(t, transport.DevshardErrorEscrowNotFound, connectDevshardError(err), "rpc=%v", err)
	}
	_, err := rpc.attach("88709", mustGenerateKey(t))
	require.Equal(t, transport.DevshardErrorEscrowLookupLimited, connectDevshardError(err), "precondition: floor is full, rpc=%v", err)

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	err = rpc.chat(escrowID, user, body)
	require.NotEqual(t, transport.DevshardErrorEscrowLookupLimited, connectDevshardError(err), "rpc=%v", err)
	meta, err := store.GetSessionMeta(escrowID)
	require.NoError(t, err, "owner's first chat must bind")
	require.Equal(t, "active", meta.Status)
}
