package event_listener

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"common/nodemanager/gen"
	"decentralized-api/apiconfig"
	"decentralized-api/nodemanager"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestHostEventStore_SQLite(t *testing.T) {
	dir := t.TempDir()
	runHostEventStoreSuite(t, func(t *testing.T, writer string) *dbStore {
		t.Helper()
		s, err := openSQLiteStore(filepath.Join(dir, hostEventDBFile), writer)
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		s.generation = 4
		return s
	})
}

func TestHostEventStore_SQLiteRecreateOnlyCorrupt(t *testing.T) {
	t.Run("not a database", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, hostEventDBFile)
		require.NoError(t, os.WriteFile(path, []byte("not a database"), 0o644))
		require.NoError(t, os.WriteFile(path+"-wal", []byte("wal-bytes"), 0o644))
		require.NoError(t, os.WriteFile(path+"-shm", []byte("shm-bytes"), 0o644))

		s, err := openSQLiteStore(path, "writer")
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, AppendExpect{Generation: 1, NextSeq: 1, PrevHeight: 0})
		require.NoError(t, err)

		kept, err := filepath.Glob(path + ".bad.*")
		require.NoError(t, err)
		require.Len(t, kept, 1)
		body, err := os.ReadFile(kept[0])
		require.NoError(t, err)
		require.Equal(t, []byte("not a database"), body)
		wal, err := os.ReadFile(path + "-wal")
		if err == nil {
			require.NotEqual(t, []byte("wal-bytes"), wal)
		}
		shm, err := os.ReadFile(path + "-shm")
		if err == nil {
			require.NotEqual(t, []byte("shm-bytes"), shm)
		}
	})

	t.Run("other error keeps the file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, hostEventDBFile)
		require.NoError(t, os.Mkdir(path, 0o755))
		require.NoError(t, os.WriteFile(path+"-wal", []byte("wal-bytes"), 0o644))

		_, err := openSQLiteStore(path, "writer")
		require.Error(t, err)
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, info.IsDir())
		body, err := os.ReadFile(path + "-wal")
		require.NoError(t, err)
		require.Equal(t, []byte("wal-bytes"), body)
		kept, err := filepath.Glob(path + ".bad.*")
		require.NoError(t, err)
		require.Empty(t, kept)
	})
}

func TestHostEventStore_EpochIndex(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	var name string
	err = s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'host_events_epoch'`).Scan(&name)
	require.NoError(t, err)
	require.Equal(t, "host_events_epoch", name)
}

func TestHostEventStore_PayloadIsProtobuf(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.generation = 4
	header := bytes.Repeat([]byte{0xff}, 32)
	events := []apiconfig.HostEvent{
		{
			Kind:           apiconfig.HostEventKindEscrowCreated,
			ObservedAtUnix: 11,
			Escrow: &apiconfig.EscrowPayload{
				EscrowID: 7, EpochIndex: 3, ModelID: "m1", Creator: "c", Amount: "100",
				Settler: "s", TotalPayout: "90", Fees: "1", Remainder: "9",
			},
		},
		{
			Kind:           apiconfig.HostEventKindMaintenanceScheduled,
			ObservedAtUnix: 12,
			Maintenance: &apiconfig.MaintenancePayload{
				ReservationID: 4, Participant: "host-a", StartHeight: 10, DurationBlocks: 5, Reason: "up",
			},
		},
		{
			Kind:           apiconfig.HostEventKindWarmKeyGranted,
			ObservedAtUnix: 13,
			WarmKey: &apiconfig.WarmKeyPayload{
				Granter: "cold", Grantee: "warm", GrantID: 2, Height: 8, ExpiresAtUnix: 99, End: "EXPIRED",
			},
		},
		{
			Kind:           apiconfig.HostEventKindBlock,
			ObservedAtUnix: 14,
			Block: &apiconfig.BlockPayload{
				Height: 1, Hash: []byte{1, 2, 3}, HeaderTimeUnixMs: 1700, EpochIndex: 3, Header: header,
			},
		},
	}
	got, err := s.AppendHeight(t.Context(), 1, 3, events, at(s, 1, 0))
	require.NoError(t, err)
	page, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindMaintenanceScheduled,
		apiconfig.HostEventKindWarmKeyGranted,
		apiconfig.HostEventKindBlock,
	}, 10)
	require.NoError(t, err)
	require.Equal(t, got, page)
	require.Equal(t, header, page[3].Block.Header)

	rows, err := s.db.Query(`SELECT payload FROM host_events WHERE writer = ? ORDER BY seq`, s.writer)
	require.NoError(t, err)
	defer rows.Close()
	n := 0
	for ; rows.Next(); n++ {
		var payload []byte
		require.NoError(t, rows.Scan(&payload))
		require.NotEmpty(t, payload)
		require.NotEqual(t, byte('{'), payload[0])
		require.NotContains(t, payload, []byte(`"Header"`))
		require.NotContains(t, payload, []byte(`"EscrowID"`))
		want, err := proto.Marshal(hostEventProto(got[n]))
		require.NoError(t, err)
		require.Equal(t, want, payload)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, len(got), n)
}

func TestHostEventStore_OldestIsLowestSeq(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.generation = 4
	_, empty, _, err := s.Oldest(t.Context())
	require.NoError(t, err)
	require.True(t, empty)
	_, _, empty, err = s.FeedHead(t.Context())
	require.NoError(t, err)
	require.True(t, empty)
	_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
	require.NoError(t, err)
	_, err = s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, 2, 1))
	require.NoError(t, err)
	seq, empty, _, err := s.Oldest(t.Context())
	require.NoError(t, err)
	require.False(t, empty)
	require.Equal(t, uint64(1), seq)
	head, gen, empty, err := s.FeedHead(t.Context())
	require.NoError(t, err)
	require.False(t, empty)
	require.Equal(t, uint64(2), head)
	require.Equal(t, uint64(4), gen)
	page, readGen, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
	require.NoError(t, err)
	require.Equal(t, uint64(4), readGen)
	require.Len(t, page, 2)
	_, found, resolveGen, err := s.ResolveBlock(t.Context(), 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(4), resolveGen)
}

func TestHostEventStore_InsertsHeightInOneStatement(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.generation = 4
	_, err = s.AppendHeight(t.Context(), 1, 1, heightRows(1, 1), at(s, 1, 0))
	require.NoError(t, err)
	require.Equal(t, 1, s.insertStmts)
	page, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindMaintenanceScheduled,
		apiconfig.HostEventKindBlock,
	}, 10)
	require.NoError(t, err)
	require.Len(t, page, 3)
}

func TestHostEventStore_ReadsDoNotWaitOnWriter(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
	require.NoError(t, err)

	s.mu.Lock()
	errCh := make(chan error, 4)
	go func() {
		_, _, err := s.Read(context.Background(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 1)
		errCh <- err
		_, _, _, err = s.ResolveBlock(context.Background(), 1)
		errCh <- err
		_, err = s.Head(context.Background())
		errCh <- err
		_, err = s.Load(context.Background())
		errCh <- err
	}()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for i := 0; i < 4; i++ {
		select {
		case err := <-errCh:
			require.NoError(t, err)
		case <-timer.C:
			s.mu.Unlock()
			t.Fatal("read blocked on the store mutex")
		}
	}
	s.mu.Unlock()
}

func TestHostEventStore_SQLiteWriterHasOwnConn(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NotNil(t, s.conn)
	require.Equal(t, sqliteReaderConns+1, s.db.Stats().MaxOpenConnections)
	_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
	require.NoError(t, err)

	held := make([]*sql.Rows, 0, sqliteReaderConns)
	t.Cleanup(func() {
		for _, rows := range held {
			_ = rows.Close()
		}
	})
	readCtx, readCancel := context.WithTimeout(t.Context(), time.Second)
	defer readCancel()
	for i := 0; i < sqliteReaderConns; i++ {
		rows, err := s.db.QueryContext(readCtx, s.q(`SELECT seq FROM host_events WHERE writer = ?`), s.writer)
		require.NoError(t, err)
		require.True(t, rows.Next())
		held = append(held, rows)
	}
	require.Equal(t, sqliteReaderConns+1, s.db.Stats().InUse)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = s.AppendHeight(ctx, 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, 2, 1))
	require.NoError(t, err)
}

func TestHostEventStore_ResolveBlockUsesPartialIndex(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.generation = 1
	next := uint64(1)
	for h := int64(1); h <= 40; h++ {
		events := heightRows(h, 1)
		_, err = s.AppendHeight(t.Context(), h, 1, events, at(s, next, h-1))
		require.NoError(t, err)
		next += uint64(len(events))
	}
	_, err = s.db.ExecContext(t.Context(), "ANALYZE")
	require.NoError(t, err)
	rows, err := s.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+s.q(resolveBlockSelect()), s.writer, int64(10))
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		if plan.Len() > 0 {
			plan.WriteByte('\n')
		}
		plan.WriteString(detail)
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan.String(), "host_events_block_height")
}

func TestHostEventStore_StoreGenerationMismatchResetsRPC(t *testing.T) {
	s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.generation = 6
	_, err = s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
	require.NoError(t, err)
	_, err = s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, 2, 1))
	require.NoError(t, err)

	ring := apiconfig.NewHostEventRing(16, 6)
	ring.Restore(6, 92, []apiconfig.HostEvent{
		{Seq: 90, Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: 90}},
		{Seq: 91, Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: 91}},
	})
	require.NoError(t, s.Reset(t.Context(), 9))
	_, err = s.AppendHeight(t.Context(), 200, 1, []apiconfig.HostEvent{
		{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 7}},
		blockOnly(200, 1),
	}, at(s, 1, 0))
	require.NoError(t, err)

	srv := nodemanager.NewServer(nil, nil, nil,
		nodemanager.WithHostEventRing(ring),
		nodemanager.WithHostEventLog(s),
		nodemanager.WithHostEventStatus(restoredLive{emitted: 91}),
	)
	resp, err := srv.GetHostEvents(t.Context(), &gen.GetHostEventsRequest{
		Cursor: 50,
		Subscribe: []gen.HostEventKind{
			gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
			gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		},
		Generation: 6,
	})
	require.NoError(t, err)
	require.True(t, resp.NeedsReset)
	require.Empty(t, resp.Events)
	require.Equal(t, uint64(9), resp.Generation)
	head, gen, empty, err := s.FeedHead(t.Context())
	require.NoError(t, err)
	require.False(t, empty)
	require.Equal(t, uint64(9), gen)
	require.Equal(t, head, resp.NextCursor)
}

func TestHostEventFeed_WriterLockMarksUnrestored(t *testing.T) {
	f, _, _ := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	f.store = lockLostStore{deadStore: deadStore{writer: "w"}}
	f.bootPending.Store(false)
	require.False(t, f.holdWriter(t.Context()))
	require.True(t, f.Stalled())
	require.False(t, f.RingRestored())
}

func TestHostEventFeed_CommitVisibleDuringPrune(t *testing.T) {
	rpc := newFeedRPC()
	rpc.results[5] = &coretypes.ResultBlockResults{
		TxsResults: []*abcitypes.ExecTxResult{
			{Events: []abcitypes.Event{abciEvent(maintenanceScheduledEvent, "",
				"reservation_id", "9", "participant", "host-a", "start_height", "100", "duration_blocks", "50")}},
		},
	}
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-commit-visible")
	var kinds []gen.HostEventKind
	s.onPrune = func() {
		srv := nodemanager.NewServer(nil, nil, nil,
			nodemanager.WithHostEventRing(ring),
			nodemanager.WithHostEventLog(s),
			nodemanager.WithHostEventStatus(f),
		)
		resp, err := srv.GetHostEvents(t.Context(), &gen.GetHostEventsRequest{
			Subscribe: []gen.HostEventKind{
				gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED,
				gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
			},
			Generation: ring.Generation(),
		})
		require.NoError(t, err)
		require.False(t, resp.NeedsReset)
		kinds = make([]gen.HostEventKind, len(resp.Events))
		for i, ev := range resp.Events {
			kinds[i] = ev.Kind
		}
	}
	bo.updateStatus(5, true)
	f.walk(t.Context())
	require.Equal(t, []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED,
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
	}, kinds)
	require.Equal(t, int64(5), f.EmittedHeight())
}

func TestHostEventFeed_PruneOnEpochChange(t *testing.T) {
	epochs := &liveEpoch{idx: 1, start: 1}
	f, bo, _ := newTestFeed(t, newFeedRPC(), epochs, nil)
	s, _ := openFeedSQLite(t, f, "writer-prune-epoch")

	bo.updateStatus(1, true)
	require.False(t, f.walk(t.Context()))
	require.Equal(t, 1, s.pruneCalls)
	_, found, _, err := s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.True(t, found)

	bo.updateStatus(2, true)
	require.False(t, f.walk(t.Context()))
	require.Equal(t, 1, s.pruneCalls)
	_, found, _, err = s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.True(t, found)

	epochs.idx = 6
	bo.updateStatus(3, true)
	require.False(t, f.walk(t.Context()))
	require.Equal(t, 2, s.pruneCalls)
	_, found, _, err = s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.False(t, found)
	_, found, _, err = s.ResolveBlock(t.Context(), 3)
	require.NoError(t, err)
	require.True(t, found)
}

type liveEpoch struct {
	idx   uint64
	start int64
}

func (e *liveEpoch) LatestEpoch() (uint64, int64, bool) { return e.idx, e.start, true }

func (e *liveEpoch) EpochStart(context.Context, uint64) (int64, error) { return e.start, nil }

func TestHostEventStore_SQLiteSynchronousFull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, hostEventDBFile)
	s, err := openSQLiteStore(path, "writer")
	require.NoError(t, err)
	defer s.Close()

	require.NotNil(t, s.conn)
	require.Equal(t, 2, pragmaSync(t, s.conn))
	require.Equal(t, "wal", pragmaJournal(t, s.conn))

	c1 := pragmaConn(t, s.db)
	c2 := pragmaConn(t, s.db)
	defer c1.Close()
	defer c2.Close()
	require.Equal(t, 2, pragmaSync(t, c1))
	require.Equal(t, 2, pragmaSync(t, c2))
	require.Equal(t, "wal", pragmaJournal(t, c1))
	require.Equal(t, "wal", pragmaJournal(t, c2))

	dirty, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer dirty.Close()
	_, err = dirty.Exec("PRAGMA synchronous=NORMAL")
	require.NoError(t, err)
	require.Equal(t, 1, pragmaSyncDB(t, dirty))

	fresh, err := sql.Open("sqlite", sqliteDSN(path))
	require.NoError(t, err)
	defer fresh.Close()
	require.Equal(t, 2, pragmaSyncDB(t, fresh))
	require.Equal(t, "wal", pragmaJournalDB(t, fresh))
}

func TestHostEventStore_Factory(t *testing.T) {
	// Stats flags do not select the feed store. An empty setting is SQLite.
	t.Setenv("DAPI_HOST_EVENTS_STORE", "")
	t.Setenv("DAPI_STATS_POSTGRES_ENABLED", "true")
	t.Setenv("DAPI_STATS_FILE_STORAGE_ENABLED", "")
	t.Setenv("PGHOST", "127.0.0.1")

	dir := t.TempDir()
	store, err := NewHostEventStore(t.Context(), HostEventStoreConfig{Writer: "w", DataDir: dir})
	require.NoError(t, err)
	defer store.Close()
	_, err = store.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, AppendExpect{Generation: 1, NextSeq: 1, PrevHeight: 0})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, hostEventDBFile))
	require.NoError(t, err)

	backend, err := hostEventBackend()
	require.NoError(t, err)
	require.Equal(t, "sqlite", backend)

	t.Setenv("DAPI_HOST_EVENTS_STORE", "postgres")
	t.Setenv("PGPORT", "1")
	t.Setenv("PGCONNECT_TIMEOUT", "1")
	backend, err = hostEventBackend()
	require.NoError(t, err)
	require.Equal(t, "postgres", backend)
	_, err = NewHostEventStore(t.Context(), HostEventStoreConfig{Writer: "w", DataDir: dir})
	require.Error(t, err)

	t.Setenv("DAPI_HOST_EVENTS_STORE", "mysql")
	_, err = hostEventBackend()
	require.Error(t, err)

	t.Setenv("DAPI_HOST_EVENTS_STORE", "sqlite")
	_, err = NewHostEventStore(t.Context(), HostEventStoreConfig{DataDir: dir})
	require.Error(t, err)
}

func TestHostEventFeed_StoreBeforeRingAndRetry(t *testing.T) {
	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-a")
	gen := ring.Generation()
	var sawStoreBeforeRing bool
	f.writeHead = func(m headMarker) error {
		if m.Height == 0 {
			return writeHeadMarkerFile(f.markerPath, m)
		}
		_, found, _, err := s.ResolveBlock(t.Context(), m.Height)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, m.Seq-1, ring.Head())
		sawStoreBeforeRing = true
		return writeHeadMarkerFile(f.markerPath, m)
	}
	bo.updateStatus(4, true)
	f.walk(t.Context())
	require.True(t, sawStoreBeforeRing)
	require.Equal(t, gen, ring.Generation())
	require.Equal(t, []int64{4}, heightsOf(ringEvents(t, ring)))

	s.failFeedUpdate = true
	bo.updateStatus(6, true)
	f.walk(t.Context())
	require.True(t, f.Stalled())
	require.Equal(t, []int64{4}, heightsOf(ringEvents(t, ring)))
	require.Equal(t, int64(4), f.emitted.Load())
	_, found, _, err := s.ResolveBlock(t.Context(), 5)
	require.NoError(t, err)
	require.False(t, found)

	require.True(t, bo.processBlock(t.Context(), 5))
	ev := <-bo.Queue.Out
	require.Equal(t, systemBarrierEventType, ev.Result.Data.Type)
	bo.signalAllEventsRead(5)
	require.Equal(t, int64(5), bo.lastProcessedBlockHeight.Load())
	require.Equal(t, gen, ring.Generation())

	s.failFeedUpdate = false
	f.walk(t.Context())
	require.False(t, f.Stalled())
	require.Equal(t, []int64{4, 5, 6}, heightsOf(ringEvents(t, ring)))
	require.Equal(t, gen, ring.Generation())
	got, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
	require.NoError(t, err)
	require.Equal(t, []uint64{got[0].Seq, got[0].Seq + 1, got[0].Seq + 2}, []uint64{got[0].Seq, got[1].Seq, got[2].Seq})
}

func TestHostEventFeed_FailedMarkerAppendsOnce(t *testing.T) {
	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-b")
	fails := 0
	f.writeHead = func(m headMarker) error {
		if m.Height > 0 && fails == 0 {
			fails++
			return errors.New("marker down")
		}
		return writeHeadMarkerFile(f.markerPath, m)
	}
	bo.updateStatus(8, true)
	f.walk(t.Context())
	require.True(t, f.Stalled())
	require.Equal(t, uint64(0), ring.Head())
	seq, found, _, err := s.ResolveBlock(t.Context(), 8)
	require.NoError(t, err)
	require.True(t, found)
	require.NotZero(t, seq)

	f.walk(t.Context())
	require.False(t, f.Stalled())
	got := ringEvents(t, ring)
	require.Equal(t, []int64{8}, heightsOf(got))
	require.Equal(t, seq, got[0].Seq)
	require.Equal(t, int64(8), got[0].Block.Height)
	again, found, _, err := s.ResolveBlock(t.Context(), 8)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, seq, again)
	rows, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestHostEventFeed_PrunedHeightNewGeneration(t *testing.T) {
	rpc := newFeedRPC()
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-c")
	bo.updateStatus(5, true)
	f.walk(t.Context())
	old := ring.Generation()
	require.Equal(t, int64(5), f.emitted.Load())

	rpc.earliest = 40
	bo.earliestUnixNano.Store(0)
	bo.updateStatus(50, true)
	f.walk(t.Context())
	require.NotEqual(t, old, ring.Generation())
	require.Equal(t, []int64{50}, heightsOf(ringEvents(t, ring)))
	_, found, _, err := s.ResolveBlock(t.Context(), 5)
	require.NoError(t, err)
	require.False(t, found)
	_, found, _, err = s.ResolveBlock(t.Context(), 50)
	require.NoError(t, err)
	require.True(t, found)
}

func TestHostEventFeed_RestartMarker(t *testing.T) {
	rpc := newFeedRPC()
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	s, dir := openFeedSQLite(t, f, "writer-d")
	bo.updateStatus(1, true)
	f.walk(t.Context())
	bo.updateStatus(2, true)
	f.walk(t.Context())
	gen := ring.Generation()
	seq1, found, _, err := s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.True(t, found)

	require.NoError(t, writeHeadMarkerFile(markerPath(dir), headMarker{
		Writer: s.Writer(), Generation: gen, Seq: seq1, Height: 1,
	}))
	f2, _, ring2 := newTestFeed(t, rpc, defaultEpochs(), nil)
	f2.store = s
	f2.markerPath = markerPath(dir)
	f2.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f2.markerPath, m) }
	bootHostEventFeed(t.Context(), f2)
	require.Equal(t, gen, ring2.Generation())
	require.Equal(t, []int64{1, 2}, heightsOf(ringEvents(t, ring2)))
	require.Equal(t, int64(2), f2.emitted.Load())

	require.NoError(t, os.Remove(markerPath(dir)))
	f3, _, ring3 := newTestFeed(t, rpc, defaultEpochs(), nil)
	f3.store = s
	f3.markerPath = markerPath(dir)
	f3.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f3.markerPath, m) }
	bootHostEventFeed(t.Context(), f3)
	require.NotEqual(t, gen, ring3.Generation())
	require.Empty(t, ringEvents(t, ring3))
	oldView := ring3.Since(1, gen, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock})
	require.True(t, oldView.Reset)
	require.Empty(t, oldView.Events)
}

func TestHostEventFeed_RestartStoreBehindMarker(t *testing.T) {
	rpc := newFeedRPC()
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	s, dir := openFeedSQLite(t, f, "writer-e")
	bo.updateStatus(1, true)
	f.walk(t.Context())
	gen := ring.Generation()
	seq, found, _, err := s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, writeHeadMarkerFile(markerPath(dir), headMarker{
		Writer: s.Writer(), Generation: gen, Seq: seq + 10, Height: 3,
	}))

	f2, _, ring2 := newTestFeed(t, rpc, defaultEpochs(), nil)
	f2.store = s
	f2.markerPath = markerPath(dir)
	f2.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f2.markerPath, m) }
	bootHostEventFeed(t.Context(), f2)
	require.NotEqual(t, gen, ring2.Generation())
	require.Empty(t, ringEvents(t, ring2))
	oldView := ring2.Since(seq, gen, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock})
	require.True(t, oldView.Reset)
	_, found, _, err = s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.False(t, found)
}

func TestHostEventFeed_FirstBootAndMissingStore(t *testing.T) {
	dir := t.TempDir()
	s, err := openSQLiteStore(filepath.Join(dir, hostEventDBFile), "writer-f")
	require.NoError(t, err)
	defer s.Close()
	f, _, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	gen := ring.Generation()
	f.store = s
	f.markerPath = markerPath(dir)
	f.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f.markerPath, m) }
	bootHostEventFeed(t.Context(), f)
	require.Equal(t, gen, ring.Generation())
	require.Empty(t, ringEvents(t, ring))

	require.NoError(t, s.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, hostEventDBFile), []byte("not a database"), 0o644))
	require.NoError(t, writeHeadMarkerFile(markerPath(dir), headMarker{Writer: "writer-f", Generation: gen, Seq: 4, Height: 4}))
	reopened, err := openSQLiteStore(filepath.Join(dir, hostEventDBFile), "writer-f")
	require.NoError(t, err)
	defer reopened.Close()
	f2, _, ring2 := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	f2.store = reopened
	f2.markerPath = markerPath(dir)
	f2.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f2.markerPath, m) }
	bootHostEventFeed(t.Context(), f2)
	require.NotEqual(t, gen, ring2.Generation())
	require.Empty(t, ringEvents(t, ring2))

	dead := deadStore{writer: "writer-f"}
	f3, _, ring3 := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	before := ring3.Generation()
	f3.store = dead
	f3.markerPath = markerPath(t.TempDir())
	f3.writeHead = func(m headMarker) error { return nil }
	bootHostEventFeed(t.Context(), f3)
	require.Equal(t, before, ring3.Generation())
	require.Empty(t, ringEvents(t, ring3))
	require.True(t, f3.Stalled())
	require.True(t, f3.bootPending.Load())
}

func TestHostEventFeed_BootRetriesBeforeAppend(t *testing.T) {
	t.Run("writer lock then restore", func(t *testing.T) {
		f, bo, ring, s := committedFeed(t)
		s.headErr = ErrWriterLocked
		bootHostEventFeed(t.Context(), f)
		require.True(t, f.bootPending.Load())
		require.True(t, f.Stalled())
		require.Equal(t, uint64(7), ring.Generation())
		require.Equal(t, uint64(0), ring.Head())

		s.headErr = nil
		bo.updateStatus(2, true)
		f.walk(t.Context())
		require.False(t, f.bootPending.Load())
		require.False(t, f.Stalled())
		require.Equal(t, uint64(4), ring.Generation())
		require.Equal(t, []int64{1, 2}, heightsOf(ringEvents(t, ring)))
		_, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)
	})

	t.Run("load failure then restore", func(t *testing.T) {
		f, bo, ring, s := committedFeed(t)
		s.loadErr = errors.New("transient")
		bootHostEventFeed(t.Context(), f)
		require.True(t, f.bootPending.Load())
		require.Equal(t, uint64(7), ring.Generation())
		require.Equal(t, uint64(0), ring.Head())

		s.loadErr = nil
		bo.updateStatus(1, true)
		f.walk(t.Context())
		require.False(t, f.bootPending.Load())
		require.False(t, f.Stalled())
		require.Equal(t, uint64(4), ring.Generation())
		require.Equal(t, []int64{1}, heightsOf(ringEvents(t, ring)))
		require.Equal(t, int64(1), f.emitted.Load())
	})

	t.Run("reset failure then new generation", func(t *testing.T) {
		f, bo, ring, s := committedFeed(t)
		require.NoError(t, writeHeadMarkerFile(f.markerPath, headMarker{
			Writer: s.Writer(), Generation: 4, Seq: 50, Height: 9,
		}))
		s.resetErr = errors.New("reset down")
		bootHostEventFeed(t.Context(), f)
		require.True(t, f.bootPending.Load())
		require.Equal(t, uint64(7), ring.Generation())
		_, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)

		s.resetErr = nil
		bo.updateStatus(10, true)
		f.walk(t.Context())
		require.False(t, f.bootPending.Load())
		require.NotEqual(t, uint64(7), ring.Generation())
		require.Equal(t, []int64{10}, heightsOf(ringEvents(t, ring)))
		_, found, _, err = s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.False(t, found)
		_, found, _, err = s.ResolveBlock(t.Context(), 10)
		require.NoError(t, err)
		require.True(t, found)
	})

	t.Run("transient head keeps rows", func(t *testing.T) {
		f, bo, ring, s := committedFeed(t)
		s.headErr = errors.New("down")
		before := ring.Generation()
		bootHostEventFeed(t.Context(), f)
		require.Equal(t, before, ring.Generation())
		require.True(t, f.bootPending.Load())
		require.True(t, f.Stalled())
		_, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)

		bo.updateStatus(3, true)
		f.walk(t.Context())
		require.Equal(t, before, ring.Generation())
		require.True(t, f.bootPending.Load())
		require.Equal(t, uint64(0), ring.Head())

		s.headErr = nil
		f.walk(t.Context())
		require.False(t, f.bootPending.Load())
		require.False(t, f.Stalled())
		require.Equal(t, uint64(4), ring.Generation())
		require.Equal(t, []int64{1, 2, 3}, heightsOf(ringEvents(t, ring)))
		_, found, _, err = s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)
	})

	t.Run("marker io error keeps rows", func(t *testing.T) {
		f, bo, ring, s := committedFeed(t)
		require.NoError(t, os.Remove(f.markerPath))
		require.NoError(t, os.Mkdir(f.markerPath, 0o755))
		before := ring.Generation()
		bootHostEventFeed(t.Context(), f)
		require.True(t, f.bootPending.Load())
		require.Equal(t, before, ring.Generation())
		_, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)

		require.NoError(t, os.Remove(f.markerPath))
		seq, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)
		require.NoError(t, writeHeadMarkerFile(f.markerPath, headMarker{
			Writer: s.Writer(), Generation: mustGeneration(t, s), Seq: seq, Height: 1,
		}))
		bo.updateStatus(2, true)
		f.walk(t.Context())
		require.False(t, f.bootPending.Load())
		require.Equal(t, uint64(4), ring.Generation())
		require.Equal(t, []int64{1, 2}, heightsOf(ringEvents(t, ring)))
	})

	t.Run("deadline keeps rows", func(t *testing.T) {
		f, bo, ring, s := committedFeed(t)
		s.waitCtx = true
		before := ring.Generation()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		bootHostEventFeed(ctx, f)
		require.True(t, f.bootPending.Load())
		require.True(t, f.Stalled())
		require.Equal(t, before, ring.Generation())
		_, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)

		s.waitCtx = false
		bo.updateStatus(2, true)
		f.walk(t.Context())
		require.False(t, f.bootPending.Load())
		require.False(t, f.Stalled())
		require.Equal(t, uint64(4), ring.Generation())
		require.Equal(t, []int64{1, 2}, heightsOf(ringEvents(t, ring)))
	})

	t.Run("canceled mismatch does not reset", func(t *testing.T) {
		f, _, ring, s := committedFeed(t)
		require.NoError(t, writeHeadMarkerFile(f.markerPath, headMarker{
			Writer: s.Writer(), Generation: 4, Seq: 50, Height: 9,
		}))
		before := ring.Generation()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		bootHostEventFeed(ctx, f)
		require.True(t, f.bootPending.Load())
		require.Equal(t, before, ring.Generation())
		_, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)
	})

	t.Run("corrupt marker resets", func(t *testing.T) {
		f, _, ring, s := committedFeed(t)
		require.NoError(t, os.WriteFile(f.markerPath, []byte("{"), 0o644))
		before := ring.Generation()
		bootHostEventFeed(t.Context(), f)
		require.False(t, f.bootPending.Load())
		require.NotEqual(t, before, ring.Generation())
		_, found, _, err := s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.False(t, found)
	})
}

func committedFeed(t *testing.T) (*HostEventFeed, *BlockObserver, *apiconfig.HostEventRing, *dbStore) {
	t.Helper()
	dir := t.TempDir()
	s, err := openSQLiteStore(filepath.Join(dir, hostEventDBFile), "writer-boot")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.generation = 4
	got, err := s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
	require.NoError(t, err)
	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	f.store = s
	f.writer = s.Writer()
	f.markerPath = markerPath(dir)
	f.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f.markerPath, m) }
	require.NoError(t, writeHeadMarkerFile(f.markerPath, headMarker{
		Writer: s.Writer(), Generation: mustGeneration(t, s), Seq: got[len(got)-1].Seq, Height: 1,
	}))
	return f, bo, ring, s
}

func TestHostEventFeed_RestartHandlersDoNotDuplicate(t *testing.T) {
	rpc := newFeedRPC()
	f, bo, _ := newTestFeed(t, rpc, defaultEpochs(), nil)
	s, dir := openFeedSQLite(t, f, "writer-g")
	for h := int64(1); h <= 5; h++ {
		bo.updateStatus(h, true)
		f.walk(t.Context())
	}

	f2, _, ring2 := newTestFeed(t, rpc, defaultEpochs(), nil)
	f2.store = s
	f2.markerPath = markerPath(dir)
	f2.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f2.markerPath, m) }
	bootHostEventFeed(t.Context(), f2)
	require.Equal(t, int64(5), f2.emitted.Load())

	handlers := NewBlockObserverWithClient(&apiconfig.ConfigManager{}, rpc)
	handlers.results = newBlockResultsCache(rpc, blockResultsCacheSize)
	handlers.lastProcessedBlockHeight.Store(2)
	handlers.lastQueriedBlockHeight.Store(2)
	handlers.updateStatus(6, true)
	for h := int64(3); h <= 5; h++ {
		require.True(t, handlers.processBlock(t.Context(), h))
		ev := <-handlers.Queue.Out
		require.Equal(t, systemBarrierEventType, ev.Result.Data.Type)
		handlers.signalAllEventsRead(h)
	}
	require.Equal(t, int64(5), handlers.lastProcessedBlockHeight.Load())

	f2.bo.updateStatus(6, true)
	f2.walk(t.Context())
	require.Equal(t, []int64{1, 2, 3, 4, 5, 6}, heightsOf(ringEvents(t, ring2)))
	for h := int64(1); h <= 6; h++ {
		_, found, _, err := s.ResolveBlock(t.Context(), h)
		require.NoError(t, err)
		require.True(t, found, "height %d", h)
	}
	rows, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 20)
	require.NoError(t, err)
	require.Len(t, rows, 6)
}

func TestHostEventFeed_StoreRetryBackoff(t *testing.T) {
	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-h")
	s.failOnce = true
	f.retryMin = 15 * time.Millisecond
	f.retryMax = 15 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go f.Run(ctx)
	bo.updateStatus(9, true)
	require.Eventually(t, func() bool { return ring.Head() > 0 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, []int64{9}, heightsOf(ringEvents(t, ring)))
	require.False(t, f.Stalled())
}

func runHostEventStoreSuite(t *testing.T, open func(t *testing.T, writer string) *dbStore) {
	t.Helper()
	t.Run("transaction", func(t *testing.T) {
		s := open(t, "tx-writer")
		s.failFeedUpdate = true
		_, err := s.AppendHeight(t.Context(), 3, 2, heightRows(3, 2), at(s, 1, 2))
		require.ErrorIs(t, err, errInjectedFeedUpdate)
		head, err := s.Head(t.Context())
		require.NoError(t, err)
		require.True(t, head.Empty)
		snap, err := s.Load(t.Context())
		require.NoError(t, err)
		require.True(t, snap.Empty)

		s.failFeedUpdate = false
		got, err := s.AppendHeight(t.Context(), 3, 2, heightRows(3, 2), at(s, 1, 2))
		require.NoError(t, err)
		require.Len(t, got, 3)
		require.Equal(t, []byte{3}, got[2].Block.Hash)
		head, err = s.Head(t.Context())
		require.NoError(t, err)
		require.Equal(t, int64(3), head.Height)
		require.Equal(t, got[2].Seq, head.Seq)
		snap, err = s.Load(t.Context())
		require.NoError(t, err)
		require.Equal(t, int64(3), snap.EmittedHeight)
		require.Equal(t, got[2].Seq+1, snap.NextSeq)

		s.failFeedUpdate = true
		_, err = s.AppendHeight(t.Context(), 4, 2, heightRows(4, 2), at(s, 4, 3))
		require.ErrorIs(t, err, errInjectedFeedUpdate)
		head, err = s.Head(t.Context())
		require.NoError(t, err)
		require.Equal(t, int64(3), head.Height)
		_, found, _, err := s.ResolveBlock(t.Context(), 4)
		require.NoError(t, err)
		require.False(t, found)
	})

	t.Run("read resolve prune", func(t *testing.T) {
		s := open(t, "read-writer")
		_, err := s.AppendHeight(t.Context(), 1, 5, heightRows(1, 5), at(s, 1, 0))
		require.NoError(t, err)
		_, err = s.AppendHeight(t.Context(), 2, 6, heightRows(2, 6), at(s, 4, 1))
		require.NoError(t, err)
		seq, found, _, err := s.ResolveBlock(t.Context(), 2)
		require.NoError(t, err)
		require.True(t, found)
		require.NotZero(t, seq)
		_, found, _, err = s.ResolveBlock(t.Context(), 9)
		require.NoError(t, err)
		require.False(t, found)

		page, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated}, 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.Equal(t, apiconfig.HostEventKindEscrowCreated, page[0].Kind)
		next, _, err := s.Read(t.Context(), page[0].Seq, []apiconfig.HostEventKind{apiconfig.HostEventKindEscrowCreated}, 10)
		require.NoError(t, err)
		require.Len(t, next, 1)
		require.Greater(t, next[0].Seq, page[0].Seq)

		require.NoError(t, s.Prune(t.Context(), 10))
		_, found, _, err = s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.False(t, found)
		_, found, _, err = s.ResolveBlock(t.Context(), 2)
		require.NoError(t, err)
		require.True(t, found)
		left, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{
			apiconfig.HostEventKindEscrowCreated,
			apiconfig.HostEventKindMaintenanceScheduled,
			apiconfig.HostEventKindBlock,
		}, 20)
		require.NoError(t, err)
		require.Len(t, left, 3)
		for _, ev := range left {
			switch ev.Kind {
			case apiconfig.HostEventKindBlock:
				require.Equal(t, int64(2), ev.Block.Height)
			case apiconfig.HostEventKindEscrowCreated:
				require.Equal(t, uint64(2), ev.Escrow.EscrowID)
			case apiconfig.HostEventKindMaintenanceScheduled:
				require.Equal(t, uint64(2), ev.Maintenance.ReservationID)
			default:
				t.Fatalf("unexpected kind %v", ev.Kind)
			}
		}
	})

	t.Run("load tail", func(t *testing.T) {
		s := open(t, "tail-writer")
		events := make([]apiconfig.HostEvent, hotTailRows+3)
		for i := range events {
			events[i] = blockOnly(1, 1)
			events[i].Block.Hash = []byte{byte(i), byte(i >> 8)}
		}
		_, err := s.AppendHeight(t.Context(), 1, 1, events, at(s, 1, 0))
		require.NoError(t, err)
		snap, err := s.Load(t.Context())
		require.NoError(t, err)
		require.Len(t, snap.Tail, hotTailRows)
		require.Equal(t, uint64(4), snap.Tail[0].Seq)
		require.Equal(t, uint64(hotTailRows+3), snap.Tail[len(snap.Tail)-1].Seq)
		require.Equal(t, snap.Tail[len(snap.Tail)-1].Seq+1, snap.NextSeq)
		require.Equal(t, int64(1), snap.EmittedHeight)
		require.Equal(t, uint64(1), snap.OldestSeq)
		require.NotZero(t, snap.Generation)

		next, err := s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, uint64(hotTailRows+3)+1, 1))
		require.NoError(t, err)
		require.Equal(t, snap.NextSeq, next[0].Seq)
	})

	t.Run("two writers", func(t *testing.T) {
		a := open(t, "alice")
		b := open(t, "bob")
		a.generation = 11
		b.generation = 22
		_, err := a.AppendHeight(t.Context(), 1, 5, heightRows(1, 5), at(a, 1, 0))
		require.NoError(t, err)
		_, err = b.AppendHeight(t.Context(), 7, 1, []apiconfig.HostEvent{blockOnly(7, 1)}, at(b, 1, 6))
		require.NoError(t, err)
		require.NoError(t, a.Prune(t.Context(), 10))

		aSnap, err := a.Load(t.Context())
		require.NoError(t, err)
		bSnap, err := b.Load(t.Context())
		require.NoError(t, err)
		require.NotEqual(t, aSnap.Generation, bSnap.Generation)
		require.Equal(t, int64(1), aSnap.EmittedHeight)
		require.Equal(t, int64(7), bSnap.EmittedHeight)
		require.Empty(t, aSnap.Tail)
		require.Len(t, bSnap.Tail, 1)
		_, found, _, err := a.ResolveBlock(t.Context(), 7)
		require.NoError(t, err)
		require.False(t, found)
		_, found, _, err = b.ResolveBlock(t.Context(), 7)
		require.NoError(t, err)
		require.True(t, found)
		page, _, err := b.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
		require.NoError(t, err)
		require.Len(t, page, 1)
		page, _, err = a.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
		require.NoError(t, err)
		require.Empty(t, page)
	})
}

func at(s *dbStore, nextSeq uint64, prevHeight int64) AppendExpect {
	gen := s.generation
	if gen == 0 {
		gen = 1
	}
	if nextSeq == 0 {
		nextSeq = 1
	}
	return AppendExpect{Generation: gen, NextSeq: nextSeq, PrevHeight: prevHeight}
}

func TestHostEventStore_AppendRejectsDivergedCursor(t *testing.T) {
	open := func(t *testing.T) *dbStore {
		t.Helper()
		s, err := openSQLiteStore(filepath.Join(t.TempDir(), hostEventDBFile), "writer")
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })
		s.generation = 4
		return s
	}

	t.Run("matching cursor continues", func(t *testing.T) {
		s := open(t)
		first, err := s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)
		again, err := s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)
		require.Equal(t, first[0].Seq, again[0].Seq)
		second, err := s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, first[0].Seq+1, 1))
		require.NoError(t, err)
		require.Equal(t, first[0].Seq+1, second[0].Seq)
	})

	t.Run("reused next seq", func(t *testing.T) {
		s := open(t)
		first, err := s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)
		_, err = s.db.ExecContext(t.Context(), `UPDATE host_event_feed SET next_seq = 50 WHERE writer = ?`, s.writer)
		require.NoError(t, err)

		_, err = s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, first[0].Seq+1, 1))
		require.ErrorIs(t, err, ErrFeedDiverged)
		_, found, _, err := s.ResolveBlock(t.Context(), 2)
		require.NoError(t, err)
		require.False(t, found)
		rows, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, first[0].Seq, rows[0].Seq)
	})

	t.Run("skipped emitted height", func(t *testing.T) {
		s := open(t)
		_, err := s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)
		_, err = s.AppendHeight(t.Context(), 4, 1, []apiconfig.HostEvent{blockOnly(4, 1)}, at(s, 2, 3))
		require.ErrorIs(t, err, ErrFeedDiverged)
		_, found, _, err := s.ResolveBlock(t.Context(), 4)
		require.NoError(t, err)
		require.False(t, found)
		_, found, _, err = s.ResolveBlock(t.Context(), 1)
		require.NoError(t, err)
		require.True(t, found)
	})

	t.Run("rolled back to empty", func(t *testing.T) {
		s := open(t)
		first, err := s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)
		_, err = s.db.ExecContext(t.Context(), `DELETE FROM host_events WHERE writer = ?`, s.writer)
		require.NoError(t, err)
		_, err = s.db.ExecContext(t.Context(), `UPDATE host_event_feed SET next_seq = 1, emitted_height = 0 WHERE writer = ?`, s.writer)
		require.NoError(t, err)

		_, err = s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, at(s, first[0].Seq+1, 1))
		require.ErrorIs(t, err, ErrFeedDiverged)
		_, found, _, err := s.ResolveBlock(t.Context(), 2)
		require.NoError(t, err)
		require.False(t, found)
		rows, _, err := s.Read(t.Context(), 0, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock}, 10)
		require.NoError(t, err)
		require.Empty(t, rows)
	})

	t.Run("wrong generation", func(t *testing.T) {
		s := open(t)
		_, err := s.AppendHeight(t.Context(), 1, 1, []apiconfig.HostEvent{blockOnly(1, 1)}, at(s, 1, 0))
		require.NoError(t, err)
		_, err = s.AppendHeight(t.Context(), 2, 1, []apiconfig.HostEvent{blockOnly(2, 1)}, AppendExpect{Generation: s.generation + 1, NextSeq: 2, PrevHeight: 1})
		require.ErrorIs(t, err, ErrFeedDiverged)
		_, found, _, err := s.ResolveBlock(t.Context(), 2)
		require.NoError(t, err)
		require.False(t, found)
		require.Equal(t, s.generation, mustGeneration(t, s))
	})

	t.Run("already emitted other generation without row", func(t *testing.T) {
		s := open(t)
		require.NoError(t, appendHeights(t, s, 1, 2))
		advanceFeed(t, s, s.generation+1, 50, 10)
		_, err := s.AppendHeight(t.Context(), 3, 1, []apiconfig.HostEvent{blockOnly(3, 1)}, at(s, 3, 2))
		require.ErrorIs(t, err, ErrFeedDiverged)
		_, found, _, err := s.ResolveBlock(t.Context(), 3)
		require.NoError(t, err)
		require.False(t, found)
	})

	t.Run("already emitted other generation with row", func(t *testing.T) {
		s := open(t)
		require.NoError(t, appendHeights(t, s, 1, 2))
		third, err := s.AppendHeight(t.Context(), 3, 1, []apiconfig.HostEvent{blockOnly(3, 1)}, at(s, 3, 2))
		require.NoError(t, err)
		advanceFeed(t, s, s.generation+1, 50, 10)
		got, err := s.AppendHeight(t.Context(), 3, 1, []apiconfig.HostEvent{blockOnly(3, 1)}, at(s, 3, 2))
		require.ErrorIs(t, err, ErrFeedDiverged)
		require.Nil(t, got)
		seq, found, _, err := s.ResolveBlock(t.Context(), 3)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, third[0].Seq, seq)
	})

	t.Run("already emitted sequence mismatch", func(t *testing.T) {
		s := open(t)
		require.NoError(t, appendHeights(t, s, 1, 2, 3))
		advanceFeed(t, s, s.generation, 50, 10)
		_, err := s.AppendHeight(t.Context(), 3, 1, []apiconfig.HostEvent{blockOnly(3, 1)}, AppendExpect{
			Generation: s.generation, NextSeq: 99, PrevHeight: 2,
		})
		require.ErrorIs(t, err, ErrFeedDiverged)
		seq, found, _, err := s.ResolveBlock(t.Context(), 3)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, uint64(3), seq)
	})
}

func TestHostEventFeed_DivergedStoreStartsNewGeneration(t *testing.T) {
	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-div")
	bo.updateStatus(1, true)
	f.walk(t.Context())
	bo.updateStatus(2, true)
	f.walk(t.Context())
	require.False(t, f.Stalled())
	oldGen := ring.Generation()
	require.Equal(t, []int64{1, 2}, heightsOf(ringEvents(t, ring)))
	require.Equal(t, uint64(2), ring.Head())

	_, err := s.db.ExecContext(t.Context(), `UPDATE host_event_feed SET next_seq = 50 WHERE writer = ?`, s.writer)
	require.NoError(t, err)
	bo.updateStatus(3, true)
	f.walk(t.Context())

	require.False(t, f.Stalled())
	require.NotEqual(t, oldGen, ring.Generation())
	got := ringEvents(t, ring)
	require.Equal(t, []int64{3}, heightsOf(got))
	require.Equal(t, uint64(1), got[0].Seq)
	require.NotEqual(t, oldGen, mustGeneration(t, s))
	old := ring.Since(2, oldGen, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock})
	require.True(t, old.Reset)
	_, found, _, err := s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.False(t, found)
	_, found, _, err = s.ResolveBlock(t.Context(), 3)
	require.NoError(t, err)
	require.True(t, found)
}

func TestHostEventFeed_AlreadyEmittedOtherGenerationStartsNew(t *testing.T) {
	t.Run("without row", func(t *testing.T) {
		testAlreadyEmittedOtherGeneration(t, false)
	})
	t.Run("with row", func(t *testing.T) {
		testAlreadyEmittedOtherGeneration(t, true)
	})
}

func testAlreadyEmittedOtherGeneration(t *testing.T, withRow bool) {
	t.Helper()
	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-ahead")
	bo.updateStatus(1, true)
	f.walk(t.Context())
	bo.updateStatus(2, true)
	f.walk(t.Context())
	require.False(t, f.Stalled())
	oldGen := ring.Generation()
	require.Equal(t, []int64{1, 2}, heightsOf(ringEvents(t, ring)))

	if withRow {
		_, err := s.AppendHeight(t.Context(), 3, 1, []apiconfig.HostEvent{blockOnly(3, 1)}, AppendExpect{
			Generation: oldGen, NextSeq: ring.Head() + 1, PrevHeight: 2,
		})
		require.NoError(t, err)
	}
	advanceFeed(t, s, oldGen+1, 50, 10)

	bo.updateStatus(10, true)
	f.walk(t.Context())

	require.False(t, f.Stalled())
	require.NotEqual(t, oldGen, ring.Generation())
	got := ringEvents(t, ring)
	require.Equal(t, []int64{10}, heightsOf(got))
	require.Equal(t, uint64(1), got[0].Seq)
	require.NotEqual(t, oldGen, mustGeneration(t, s))
	old := ring.Since(2, oldGen, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock})
	require.True(t, old.Reset)
	_, found, _, err := s.ResolveBlock(t.Context(), 1)
	require.NoError(t, err)
	require.False(t, found)
	_, found, _, err = s.ResolveBlock(t.Context(), 3)
	require.NoError(t, err)
	require.False(t, found)
	_, found, _, err = s.ResolveBlock(t.Context(), 10)
	require.NoError(t, err)
	require.True(t, found)
}

func appendHeights(t *testing.T, s *dbStore, heights ...int64) error {
	t.Helper()
	next := uint64(1)
	var prev int64
	for _, h := range heights {
		if _, err := s.AppendHeight(t.Context(), h, 1, []apiconfig.HostEvent{blockOnly(h, 1)}, at(s, next, prev)); err != nil {
			return err
		}
		next++
		prev = h
	}
	return nil
}

func advanceFeed(t *testing.T, s *dbStore, generation, nextSeq uint64, emitted int64) {
	t.Helper()
	_, err := s.db.ExecContext(t.Context(),
		`UPDATE host_event_feed SET generation = ?, next_seq = ?, emitted_height = ? WHERE writer = ?`,
		generation, nextSeq, emitted, s.writer)
	require.NoError(t, err)
}

func openFeedSQLite(t *testing.T, f *HostEventFeed, writer string) (*dbStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := openSQLiteStore(filepath.Join(dir, hostEventDBFile), writer)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	f.store = s
	f.writer = s.Writer()
	f.markerPath = markerPath(dir)
	f.writeHead = func(m headMarker) error { return writeHeadMarkerFile(f.markerPath, m) }
	bootHostEventFeed(t.Context(), f)
	return s, dir
}

func heightRows(height int64, epoch uint64) []apiconfig.HostEvent {
	return []apiconfig.HostEvent{
		{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: uint64(height), EpochIndex: epoch, ModelID: "m"}},
		{Kind: apiconfig.HostEventKindMaintenanceScheduled, Maintenance: &apiconfig.MaintenancePayload{ReservationID: uint64(height), Participant: "host-a"}},
		blockOnly(height, epoch),
	}
}

func blockOnly(height int64, epoch uint64) apiconfig.HostEvent {
	return apiconfig.HostEvent{
		Kind: apiconfig.HostEventKindBlock,
		Block: &apiconfig.BlockPayload{
			Height:           height,
			EpochIndex:       epoch,
			Hash:             []byte{byte(height)},
			HeaderTimeUnixMs: height * 1000,
			Header:           []byte{9, 9},
		},
	}
}

func pragmaConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(t.Context())
	require.NoError(t, err)
	return c
}

func pragmaSync(t *testing.T, c *sql.Conn) int {
	t.Helper()
	var v int
	require.NoError(t, c.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&v))
	return v
}

func pragmaJournal(t *testing.T, c *sql.Conn) string {
	t.Helper()
	var v string
	require.NoError(t, c.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&v))
	return v
}

func pragmaSyncDB(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&v))
	return v
}

func pragmaJournalDB(t *testing.T, db *sql.DB) string {
	t.Helper()
	var v string
	require.NoError(t, db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&v))
	return v
}

type restoredLive struct{ emitted int64 }

func (r restoredLive) Stalled() bool        { return false }
func (r restoredLive) EmittedHeight() int64 { return r.emitted }
func (r restoredLive) TipHeight() int64     { return r.emitted }
func (r restoredLive) RingRestored() bool   { return true }

type lockLostStore struct{ deadStore }

func (lockLostStore) HoldWriter(context.Context) error { return ErrWriterLocked }

type deadStore struct{ writer string }

func (d deadStore) Writer() string                   { return d.writer }
func (d deadStore) HoldWriter(context.Context) error { return nil }
func (d deadStore) AppendHeight(context.Context, int64, uint64, []apiconfig.HostEvent, AppendExpect) ([]apiconfig.HostEvent, error) {
	return nil, errors.New("unreadable")
}
func (d deadStore) Oldest(context.Context) (uint64, bool, uint64, error) {
	return 0, true, 0, errors.New("unreadable")
}
func (d deadStore) Read(context.Context, uint64, []apiconfig.HostEventKind, int) ([]apiconfig.HostEvent, uint64, error) {
	return nil, 0, errors.New("unreadable")
}
func (d deadStore) ResolveBlock(context.Context, int64) (uint64, bool, uint64, error) {
	return 0, false, 0, errors.New("unreadable")
}
func (d deadStore) Prune(context.Context, uint64) error { return errors.New("unreadable") }
func (d deadStore) Load(context.Context) (HostEventSnapshot, error) {
	return HostEventSnapshot{}, errors.New("unreadable")
}
func (d deadStore) Head(context.Context) (StoreHead, error) {
	return StoreHead{}, errors.New("unreadable")
}
func (d deadStore) FeedHead(context.Context) (uint64, uint64, bool, error) {
	return 0, 0, true, errors.New("unreadable")
}
func (d deadStore) Reset(context.Context, uint64) error { return errors.New("unreadable") }
func (d deadStore) Close() error                        { return nil }
