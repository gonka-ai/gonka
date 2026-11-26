package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"devshard/storage/migrate"
	"github.com/stretchr/testify/require"
)

func TestPostgresPeerSessionSecretSurvivesReplicasAndRestart(t *testing.T) {
	pg := newTestPostgres(t)
	key, available, err := SharedPeerSessionKey(pg, "cold-host", "v7", 1)
	require.NoError(t, err)
	require.True(t, available)
	require.Len(t, key, 32)
	// On an empty table, simultaneous replicas must all choose the winning seed.
	// This DB has no sessions/servers; deleting here models first deployment.
	_, err = pg.pool.Exec(context.Background(), "DELETE FROM devshard_peer_rpc_secret")
	require.NoError(t, err)
	var wg sync.WaitGroup
	seeds := make(chan []byte, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seed, err := loadPeerSessionSecret(context.Background(), pg.pool)
			if err != nil {
				t.Error(err)
				return
			}
			seeds <- seed
		}()
	}
	wg.Wait()
	close(seeds)
	var winner []byte
	for seed := range seeds {
		require.Len(t, seed, 32)
		if winner == nil {
			winner = seed
		} else {
			require.Equal(t, winner, seed)
		}
	}
	require.Len(t, winner, 32)
	pg.peerSessionSecret = winner // no live server; use the winning first-deployment seed
	key, _, err = SharedPeerSessionKey(pg, "cold-host", "v7", 1)
	require.NoError(t, err)
	second, err := NewPostgres(context.Background())
	require.NoError(t, err)
	wrapped := NewObsRepairGate(NewManagedStorage(newHybridRouter(nil, second, true, ""), 0, nil))
	got, available, err := SharedPeerSessionKey(wrapped, "cold-host", "v7", 1)
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, key, got)
	got[0] ^= 1
	require.NoError(t, second.Close())
	third, err := NewPostgres(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = third.Close() })
	again, _, err := SharedPeerSessionKey(third, "cold-host", "v7", 1)
	require.NoError(t, err)
	require.Equal(t, key, again)
	// An old v6 binary ignores this additive migration when sharing the DB.
	require.NoError(t, migrate.ApplyPG(context.Background(), pg.pool, postgresMigrationSteps[:len(postgresMigrationSteps)-1]))
}
func TestSharedPeerSessionKeyWithoutPostgres(t *testing.T) {
	store := NewObsRepairGate(NewManagedStorage(newHybridRouter(NewMemory(), nil, false, ""), 0, nil))
	key, available, err := SharedPeerSessionKey(store, "host", "v7", 1)
	require.NoError(t, err)
	require.False(t, available)
	require.Nil(t, key)
	for _, router := range []*HybridStorage{newHybridRouter(NewMemory(), nil, true, ""), newHybridRouter(NewMemory(), nil, false, "")} {
		router.degradedOwnerOnly = true
		_, available, err = SharedPeerSessionKey(router, "host", "v7", 1)
		require.True(t, available)
		require.Error(t, err)
	}
	// A configured but invalid seed must fail closed, not fall back to a signer.
	key, available, err = SharedPeerSessionKey(&Postgres{}, "host", "v7", 1)
	require.Error(t, err)
	require.True(t, available)
	require.Nil(t, key)
}

func TestPostgresPeerSessionSecretFailureClosesFence(t *testing.T) {
	pg := newTestPostgres(t)
	ctx := context.Background()
	countLocks := func() int {
		var n int
		err := pg.pool.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=$1::bigint::oid AND objsubid=2 AND granted", postgresConnectionGuardNamespace).Scan(&n)
		require.NoError(t, err)
		return n
	}
	before := countLocks()
	tx, err := pg.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "LOCK TABLE devshard_peer_rpc_secret IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)
	// Cancel startup while it waits for the seed, after arming its own fence.
	startup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	broken, err := NewPostgres(startup)
	require.Error(t, err)
	require.Nil(t, broken)
	require.Eventually(t, func() bool { return countLocks() == before }, time.Second, 10*time.Millisecond, "failed startup must close its independent fence connection")
}
