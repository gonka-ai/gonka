package storage

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestStorageProofUsesLiveSharedPostgres(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := setupDevshardPostgresPool(t, nil)
	defer cleanup()
	require.NoError(t, MigratePostgres(ctx, pool))

	first := &Postgres{pool: pool}
	peer := &Postgres{pool: pool}
	nonce := uuid.NewString()
	written, err := first.StorageProof(ctx, ProofWriteChallenge, nonce)
	require.NoError(t, err)
	require.True(t, written.Found)
	observed, err := peer.StorageProof(ctx, ProofReadChallenge, nonce)
	require.NoError(t, err)
	require.True(t, observed.Found)
	require.Equal(t, written.Identity, observed.Identity)
}

func TestStorageProofRejectsClonedIdentityOnIndependentDatabase(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := setupDevshardPostgresPool(t, nil)
	defer cleanup()
	require.NoError(t, MigratePostgres(ctx, pool))

	_, err := pool.Exec(ctx, `CREATE DATABASE storage_proof_clone`)
	require.NoError(t, err)
	cloneConfig := pool.Config().Copy()
	cloneConfig.ConnConfig.Database = "storage_proof_clone"
	clonePool, err := pgxpool.NewWithConfig(ctx, cloneConfig)
	require.NoError(t, err)
	defer clonePool.Close()
	require.NoError(t, clonePool.Ping(ctx))
	require.NoError(t, MigratePostgres(ctx, clonePool))

	var identity string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT identity::text FROM devshard_storage_identity WHERE singleton`).Scan(&identity))
	_, err = clonePool.Exec(ctx, `
		UPDATE devshard_storage_identity SET identity = $1::uuid WHERE singleton`, identity)
	require.NoError(t, err)

	nonce := uuid.NewString()
	_, err = (&Postgres{pool: pool}).StorageProof(ctx, ProofWriteChallenge, nonce)
	require.NoError(t, err)
	cloneProof, err := (&Postgres{pool: clonePool}).StorageProof(ctx, ProofReadChallenge, nonce)
	require.NoError(t, err)
	require.Equal(t, identity, cloneProof.Identity, "lineage marker was deliberately cloned")
	require.False(t, cloneProof.Found, "live challenge must not cross independent databases")
}

func TestPostgresConnectionGuardRejectsIndependentClone(t *testing.T) {
	ctx := context.Background()
	bootstrapPool, cleanup := setupDevshardPostgresPool(t, nil)
	defer cleanup()
	require.NoError(t, MigratePostgres(ctx, bootstrapPool))

	_, err := bootstrapPool.Exec(ctx, `CREATE DATABASE connection_guard_clone`)
	require.NoError(t, err)
	cloneConfig := bootstrapPool.Config().Copy()
	cloneConfig.ConnConfig.Database = "connection_guard_clone"
	clonePool, err := pgxpool.NewWithConfig(ctx, cloneConfig)
	require.NoError(t, err)
	require.NoError(t, MigratePostgres(ctx, clonePool))

	var identity string
	require.NoError(t, bootstrapPool.QueryRow(ctx, `
		SELECT identity::text FROM devshard_storage_identity WHERE singleton`).Scan(&identity))
	_, err = clonePool.Exec(ctx, `
		UPDATE devshard_storage_identity SET identity = $1::uuid WHERE singleton`, identity)
	require.NoError(t, err)
	clonePool.Close()

	guard, err := newPostgresConnectionGuard()
	require.NoError(t, err)
	primaryConfig := bootstrapPool.Config().Copy()
	guard.installValidator(primaryConfig)
	primaryPool, err := pgxpool.NewWithConfig(ctx, primaryConfig)
	require.NoError(t, err)
	defer primaryPool.Close()
	require.NoError(t, guard.arm(ctx, primaryPool, primaryConfig.ConnConfig))
	defer guard.close(ctx)
	require.NoError(t, primaryPool.Ping(ctx), "the database containing the process token must remain usable")

	guardedCloneConfig := cloneConfig.Copy()
	guard.installValidator(guardedCloneConfig)
	guardedClonePool, err := pgxpool.NewWithConfig(ctx, guardedCloneConfig)
	require.NoError(t, err)
	defer guardedClonePool.Close()
	require.Error(t, guardedClonePool.Ping(ctx),
		"a clone with the same durable identity but without the session fence must be rejected")

	require.NoError(t, guard.fenceConn.Close(ctx))
	guard.fenceConn = nil
	primaryPool.Reset()
	require.Error(t, primaryPool.Ping(ctx),
		"new connections must fail closed after the non-replicated fence session disappears")
}

func TestStorageProofRejectsInvalidNonce(t *testing.T) {
	_, err := (&Postgres{}).StorageProof(context.Background(), ProofReadChallenge, "not-a-uuid")
	require.Error(t, err)
}

func TestStorageProofRequiresWritablePostgres(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := setupDevshardPostgresPool(t, nil)
	defer cleanup()
	require.NoError(t, MigratePostgres(ctx, pool))

	readOnlyConfig := pool.Config().Copy()
	readOnlyConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	readOnlyPool, err := pgxpool.NewWithConfig(ctx, readOnlyConfig)
	require.NoError(t, err)
	defer readOnlyPool.Close()
	require.NoError(t, readOnlyPool.Ping(ctx))

	_, err = (&Postgres{pool: readOnlyPool}).StorageProof(ctx, ProofWriteChallenge, uuid.NewString())
	require.Error(t, err)
}
