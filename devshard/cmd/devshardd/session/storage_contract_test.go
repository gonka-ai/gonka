package session

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/storage"
)

type storageContractProbe struct {
	storage.Storage
	proof func(context.Context, storage.ProofOperation, string) (storage.StorageProof, error)
	fatal chan error
}

func (s *storageContractProbe) StorageProof(ctx context.Context, operation storage.ProofOperation, nonce string) (storage.StorageProof, error) {
	return s.proof(ctx, operation, nonce)
}

func (s *storageContractProbe) FatalErrors() <-chan error {
	return s.fatal
}

func TestHostManagerStorageProofThroughObsRepairGate(t *testing.T) {
	backend := &storageContractProbe{Storage: storage.NewMemory()}
	// Match the production retention wrapper followed by NewHostManager's
	// observation-repair gate. Deployment probes see only the outer store.
	managed := storage.NewManagedStorage(backend, 3, nil)
	mgr := NewHostManager(managed, nil, nil, nil, nil, "v5", nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const nonce = "e76a4079-785f-43ea-94b2-b6773db1b565"
	want := storage.StorageProof{
		Identity: "live-database", Found: true,
		PoolMaxConnections: 4, ServerMaxConnections: 100, ServerReservedConnections: 3,
	}
	for _, operation := range []storage.ProofOperation{
		storage.ProofIdentity, storage.ProofWriteChallenge, storage.ProofReadChallenge,
	} {
		t.Run(string(operation), func(t *testing.T) {
			backend.proof = func(gotCtx context.Context, gotOperation storage.ProofOperation, gotNonce string) (storage.StorageProof, error) {
				require.Same(t, ctx, gotCtx)
				require.Equal(t, operation, gotOperation)
				require.Equal(t, nonce, gotNonce)
				return want, nil
			}
			got, err := mgr.StorageProof(ctx, operation, nonce)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}

	backend.proof = func(ctx context.Context, _ storage.ProofOperation, _ string) (storage.StorageProof, error) {
		return storage.StorageProof{}, ctx.Err()
	}
	cancel()
	got, err := mgr.StorageProof(ctx, storage.ProofIdentity, "")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, got, "an unavailable backend must not produce a successful identity proof")
}

func TestHostManagerStorageFatalErrorsThroughObsRepairGate(t *testing.T) {
	backend := &storageContractProbe{Storage: storage.NewMemory(), fatal: make(chan error, 1)}
	managed := storage.NewManagedStorage(backend, 3, nil)
	mgr := NewHostManager(managed, nil, nil, nil, nil, "v5", nil, nil, nil)
	fatalErrors := mgr.StorageFatalErrors()
	require.NotNil(t, fatalErrors, "the process shutdown watcher must receive the backend fatal channel")
	require.Equal(t, (<-chan error)(backend.fatal), fatalErrors)

	// A repair in progress must not delay the fence-loss notification that
	// requires devshardd to exit and versiond to replace it.
	err := mgr.obsGate.RepairValidationObs("escrow-1", func(storage.Storage) error {
		fenceLost := errors.New("postgres fence session lost")
		backend.fatal <- fenceLost
		select {
		case got := <-fatalErrors:
			require.ErrorIs(t, got, fenceLost)
		default:
			t.Fatal("the fatal storage error did not reach the process shutdown watcher")
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, fatalErrors, mgr.StorageFatalErrors(), "the fatal channel must remain stable across repair")
}

func TestHostManagerStorageWithoutOptionalContracts(t *testing.T) {
	for _, managed := range []bool{false, true} {
		name := "direct"
		var store storage.Storage = storage.NewMemory()
		if managed {
			name = "managed"
			store = storage.NewManagedStorage(store, 3, nil)
		}
		t.Run(name, func(t *testing.T) {
			mgr := NewHostManager(store, nil, nil, nil, nil, "v5", nil, nil, nil)
			require.True(t, mgr.StorageReady())
			require.Nil(t, mgr.StorageFatalErrors())
			proof, err := mgr.StorageProof(context.Background(), storage.ProofIdentity, "")
			require.EqualError(t, err, "postgres storage proof is unavailable")
			require.Empty(t, proof)
		})
	}
}
