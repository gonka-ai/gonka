package payloads

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type memPayloadDest struct {
	mu   sync.Mutex
	data map[string][2][]byte
}

func newMemPayloadDest() *memPayloadDest {
	return &memPayloadDest{data: make(map[string][2][]byte)}
}

func payloadDestKey(escrow string, inference, epoch uint64) string {
	return strconv.FormatUint(epoch, 10) + "/" + escrow + "/" + strconv.FormatUint(inference, 10)
}

func (m *memPayloadDest) Store(_ context.Context, escrowId string, inferenceId, epochId uint64, prompt, response []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[payloadDestKey(escrowId, inferenceId, epochId)] = [2][]byte{prompt, response}
	return nil
}

func (m *memPayloadDest) Retrieve(_ context.Context, escrowId string, inferenceId, epochId uint64) ([]byte, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[payloadDestKey(escrowId, inferenceId, epochId)]
	if !ok {
		return nil, nil, ErrNotFound
	}
	return v[0], v[1], nil
}

func (m *memPayloadDest) DropEpoch(_ context.Context, epochId uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := strconv.FormatUint(epochId, 10) + "/"
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(m.data, k)
		}
	}
	return nil
}

func TestMigrateFilePayloadsToPostgres(t *testing.T) {
	dir := t.TempDir()
	src := NewFileStorage(dir)
	ctx := context.Background()

	require.NoError(t, src.Store(ctx, "esc-a", 1, 3, []byte("p1"), []byte("r1")))
	require.NoError(t, src.Store(ctx, "esc-a", 2, 3, []byte("p2"), []byte("r2")))
	require.NoError(t, src.Store(ctx, "esc-b", 9, 4, []byte("pb"), []byte("rb")))

	dest := newMemPayloadDest()
	n, err := MigrateFilePayloadsToPostgres(ctx, dir, dest)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	prompt, resp, err := dest.Retrieve(ctx, "esc-a", 1, 3)
	require.NoError(t, err)
	require.Equal(t, []byte("p1"), prompt)
	require.Equal(t, []byte("r1"), resp)

	prompt, resp, err = dest.Retrieve(ctx, "esc-b", 9, 4)
	require.NoError(t, err)
	require.Equal(t, []byte("pb"), prompt)
	require.Equal(t, []byte("rb"), resp)

	// Epoch trees quarantined after success.
	_, err = os.Stat(filepath.Join(dir, "3"))
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(dir, "4"))
	require.True(t, os.IsNotExist(err))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	quarantined := 0
	for _, e := range entries {
		if e.IsDir() {
			quarantined++
		}
	}
	require.Equal(t, 2, quarantined)
}

func TestMigrateFilePayloadsToPostgres_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	dest := newMemPayloadDest()
	n, err := MigrateFilePayloadsToPostgres(context.Background(), dir, dest)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestMigrateFilePayloadsToPostgres_MissingDir(t *testing.T) {
	dest := newMemPayloadDest()
	n, err := MigrateFilePayloadsToPostgres(context.Background(), filepath.Join(t.TempDir(), "nope"), dest)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}
