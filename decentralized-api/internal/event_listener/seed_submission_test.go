package event_listener

import (
	"context"
	"errors"
	"sync"
	"testing"

	"decentralized-api/apiconfig"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

const seedTestParticipant = "gonka1seedtestparticipant"

type seedQueryResult struct {
	response *types.QueryRandomSeedsResponse
	err      error
}

type seedQueryClient struct {
	mu      sync.Mutex
	results []seedQueryResult
	calls   int
}

func (q *seedQueryClient) EpochInfo(context.Context, *types.QueryEpochInfoRequest, ...grpc.CallOption) (*types.QueryEpochInfoResponse, error) {
	return nil, errors.New("not implemented")
}

func (q *seedQueryClient) Params(context.Context, *types.QueryParamsRequest, ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	return nil, errors.New("not implemented")
}

func (q *seedQueryClient) ListRandomSeeds(context.Context, *types.QueryRandomSeedsRequest, ...grpc.CallOption) (*types.QueryRandomSeedsResponse, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.calls++
	if len(q.results) == 0 {
		return &types.QueryRandomSeedsResponse{}, nil
	}
	index := q.calls - 1
	if index >= len(q.results) {
		index = len(q.results) - 1
	}
	return q.results[index].response, q.results[index].err
}

type recordingSeedManager struct {
	mu          sync.Mutex
	config      *apiconfig.ConfigManager
	generated   []uint64
	createdSeed apiconfig.SeedInfo
	createErr   error
}

func (m *recordingSeedManager) GenerateSeedInfo(epochIndex uint64) {
	m.mu.Lock()
	m.generated = append(m.generated, epochIndex)
	m.mu.Unlock()

	seed := m.createdSeed
	seed.EpochIndex = epochIndex
	_ = m.config.SetUpcomingSeed(seed)
}

func (m *recordingSeedManager) GetSeedForEpoch(uint64) apiconfig.SeedInfo {
	return apiconfig.SeedInfo{}
}

func (m *recordingSeedManager) CreateNewSeed(epochIndex uint64) (*apiconfig.SeedInfo, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	seed := m.createdSeed
	seed.EpochIndex = epochIndex
	return &seed, nil
}

func (m *recordingSeedManager) ChangeCurrentSeed() {}

func (m *recordingSeedManager) RequestMoney(uint64) {}

func (m *recordingSeedManager) generatedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.generated)
}

func seedTestEpoch() types.EpochContext {
	return types.EpochContext{
		EpochIndex:          2,
		PocStartBlockHeight: 100,
		EpochParams: types.EpochParams{
			PocStageDuration:      20,
			PocValidationDelay:    2,
			PocValidationDuration: 10,
		},
	}
}

func newSeedTestDispatcher(results ...seedQueryResult) (*OnNewBlockDispatcher, *recordingSeedManager, *apiconfig.ConfigManager) {
	config := &apiconfig.ConfigManager{}
	manager := &recordingSeedManager{
		config: config,
		createdSeed: apiconfig.SeedInfo{
			Seed:      42,
			Signature: "deterministic-signature",
		},
	}
	return &OnNewBlockDispatcher{
		queryClient:       &seedQueryClient{results: results},
		randomSeedManager: manager,
		configManager:     config,
	}, manager, config
}

func chainSeed(signature string) seedQueryResult {
	return seedQueryResult{response: &types.QueryRandomSeedsResponse{
		Seeds: []*types.RandomSeed{{
			Participant: seedTestParticipant,
			EpochIndex:  2,
			Signature:   signature,
		}},
	}}
}

func emptySeedQuery() seedQueryResult {
	return seedQueryResult{response: &types.QueryRandomSeedsResponse{}}
}

func TestSeedSubmissionWindow(t *testing.T) {
	dispatcher, _, _ := newSeedTestDispatcher()
	epoch := seedTestEpoch()

	require.True(t, dispatcher.isSeedSubmissionWindow(epoch, 100))
	require.True(t, dispatcher.isSeedSubmissionWindow(epoch, 131))
	require.False(t, dispatcher.isSeedSubmissionWindow(epoch, 99))
	require.False(t, dispatcher.isSeedSubmissionWindow(epoch, 132))
	require.False(t, dispatcher.isSeedSubmissionWindow(types.EpochContext{}, 0))
}

func TestEnsureSeedSubmittedRecoversAfterBoundaryLag(t *testing.T) {
	dispatcher, manager, _ := newSeedTestDispatcher(emptySeedQuery())
	oldEpoch := types.EpochContext{
		EpochIndex:          1,
		PocStartBlockHeight: 0,
		EpochParams:         seedTestEpoch().EpochParams,
	}

	require.False(t, dispatcher.isSeedSubmissionWindow(oldEpoch, 100))
	require.True(t, dispatcher.isSeedSubmissionWindow(seedTestEpoch(), 101))

	dispatcher.ensureSeedSubmitted(context.Background(), seedTestEpoch(), 101, seedTestParticipant)
	require.Equal(t, 1, manager.generatedCount())
}

func TestEnsureSeedSubmittedConfirmsAfterVisibilityDelay(t *testing.T) {
	dispatcher, manager, _ := newSeedTestDispatcher(
		emptySeedQuery(),
		chainSeed("deterministic-signature"),
	)
	epoch := seedTestEpoch()

	dispatcher.ensureSeedSubmitted(context.Background(), epoch, 100, seedTestParticipant)
	dispatcher.ensureSeedSubmitted(context.Background(), epoch, 101, seedTestParticipant) // cooldown: no query
	dispatcher.ensureSeedSubmitted(context.Background(), epoch, 102, seedTestParticipant)

	require.Equal(t, 1, manager.generatedCount())
	require.Equal(t, uint64(2), dispatcher.seedConfirmedEpoch)
}

func TestEnsureSeedSubmittedRetriesAfterCooldown(t *testing.T) {
	dispatcher, manager, _ := newSeedTestDispatcher(emptySeedQuery())
	epoch := seedTestEpoch()

	dispatcher.ensureSeedSubmitted(context.Background(), epoch, 100, seedTestParticipant)
	dispatcher.ensureSeedSubmitted(context.Background(), epoch, 101, seedTestParticipant)
	dispatcher.ensureSeedSubmitted(context.Background(), epoch, 102, seedTestParticipant)

	require.Equal(t, 2, manager.generatedCount())
}

func TestEnsureSeedSubmittedRepairsPersistedButUnconfirmedSeed(t *testing.T) {
	dispatcher, manager, config := newSeedTestDispatcher(emptySeedQuery())
	require.NoError(t, config.SetUpcomingSeed(apiconfig.SeedInfo{
		Seed:       42,
		EpochIndex: 2,
		Signature:  "deterministic-signature",
	}))

	dispatcher.ensureSeedSubmitted(context.Background(), seedTestEpoch(), 101, seedTestParticipant)

	require.Equal(t, 1, manager.generatedCount())
}

func TestEnsureSeedSubmittedRestoresMissingLocalSeed(t *testing.T) {
	dispatcher, manager, config := newSeedTestDispatcher(chainSeed("deterministic-signature"))

	dispatcher.ensureSeedSubmitted(context.Background(), seedTestEpoch(), 102, seedTestParticipant)

	require.Equal(t, 0, manager.generatedCount())
	require.Equal(t, uint64(2), config.GetUpcomingSeed().EpochIndex)
	require.Equal(t, "deterministic-signature", config.GetUpcomingSeed().Signature)
}

func TestEnsureSeedSubmittedDoesNotOverwriteSignatureMismatch(t *testing.T) {
	dispatcher, manager, config := newSeedTestDispatcher(chainSeed("different-signature"))
	require.NoError(t, config.SetUpcomingSeed(apiconfig.SeedInfo{
		Seed:       42,
		EpochIndex: 2,
		Signature:  "deterministic-signature",
	}))

	dispatcher.ensureSeedSubmitted(context.Background(), seedTestEpoch(), 102, seedTestParticipant)

	require.Equal(t, 0, manager.generatedCount())
	require.Equal(t, "deterministic-signature", config.GetUpcomingSeed().Signature)
	require.Equal(t, uint64(2), dispatcher.seedConfirmedEpoch)
}

func TestEnsureSeedSubmittedFailsOpenOnQueryError(t *testing.T) {
	dispatcher, manager, _ := newSeedTestDispatcher(seedQueryResult{err: errors.New("query unavailable")})

	dispatcher.ensureSeedSubmitted(context.Background(), seedTestEpoch(), 100, seedTestParticipant)

	require.Equal(t, 1, manager.generatedCount())
}

func TestEnsureSeedSubmittedSerializesDuplicateEvents(t *testing.T) {
	dispatcher, manager, _ := newSeedTestDispatcher(emptySeedQuery())

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dispatcher.ensureSeedSubmitted(context.Background(), seedTestEpoch(), 100, seedTestParticipant)
		}()
	}
	wg.Wait()

	require.Equal(t, 1, manager.generatedCount())
}
