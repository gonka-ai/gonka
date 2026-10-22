package harness

import (
	"database/sql"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// testdata/escrow_replay holds the session row and the first 30 diffs of one host's chain from a real
// run: genuine user signatures and post_state_roots, inferences started, finished and in flight.
const escrowReplayFixture = "testdata/escrow_replay"

func TestReplayBalancesOnARealChain(t *testing.T) {
	r, err := ReplayBalances(escrowReplayFixture)
	require.NoError(t, err)
	require.Len(t, r, 1)
	b := r[0]
	require.Equal(t, "primary", b.Host)
	require.Empty(t, b.Stopped)
	require.Equal(t, 30, b.Replayed)
	require.Len(t, b.Points, 30)
	for _, p := range b.Points {
		require.Equal(t, p.Amount, p.Sum(), "nonce %d", p.Nonce)
	}
	last := b.Points[29]
	require.Equal(t, uint64(30), last.Nonce)
	require.Equal(t, uint64(5000000000), last.Amount)
	require.NotZero(t, last.Fees)
	require.NotZero(t, last.Charged, "the fixture has finished inferences")
	require.NotZero(t, last.Reserved, "the fixture has inferences in flight")
	require.Equal(t, last.Amount, last.Sum())
}

// copyFixture copies the fixture and lets the test change one diff row before the copy is written.
func copyFixture(t *testing.T, change func(row []string, header []string)) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "postgres")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	raw, err := os.ReadFile(filepath.Join(escrowReplayFixture, "postgres", "public.devshard_sessions.csv"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "public.devshard_sessions.csv"), raw, 0o644))
	f, err := os.Open(filepath.Join(escrowReplayFixture, "postgres", "public.devshard_diffs.csv"))
	require.NoError(t, err)
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	require.NoError(t, err)
	for _, row := range rows[1:] {
		change(row, rows[0])
	}
	out, err := os.Create(filepath.Join(dir, "public.devshard_diffs.csv"))
	require.NoError(t, err)
	w := csv.NewWriter(out)
	require.NoError(t, w.WriteAll(rows))
	require.NoError(t, out.Close())
	return filepath.Dir(dir)
}

func column(header []string, name string) int {
	for i, h := range header {
		if h == name {
			return i
		}
	}
	return -1
}

func TestReplayStopsWhereAUserSignatureNoLongerVerifies(t *testing.T) {
	dir := copyFixture(t, func(row, header []string) {
		if row[column(header, "nonce")] == "12" {
			root := []byte(row[column(header, "post_state_root")])
			if root[len(root)-1] == '0' { // another hex digit, so the root still decodes
				root[len(root)-1] = '1'
			} else {
				root[len(root)-1] = '0'
			}
			row[column(header, "post_state_root")] = string(root)
		}
	})
	r, err := ReplayBalances(dir)
	require.NoError(t, err)
	require.Equal(t, 11, r[0].Replayed)
	require.Contains(t, r[0].Stopped, "nonce 12")
	require.Contains(t, r[0].Stopped, "signature", "the user signs the post_state_root: a changed root breaks the signature first")

	rep := PromiseReport{Checked: map[string]Measure{}}
	checkBalance(PromiseInput{Balances: r}, &rep)
	require.True(t, rep.Checked[EscrowMoneyConserved].Measured, "the 11 diffs before the stop are judged")
	require.Len(t, rep.Observations, 2)
	require.Contains(t, rep.Observations[0], "primary/2 after 11 diffs")
	require.Contains(t, rep.Observations[1], "0 timeout refunds, 0 invalidations", "the fixture takes no refund path")
}

func TestReplayStopsOnAGap(t *testing.T) {
	dir := copyFixture(t, func(row, header []string) {
		if row[column(header, "nonce")] == "1" {
			row[column(header, "nonce")] = "100"
		}
	})
	r, err := ReplayBalances(dir)
	require.NoError(t, err)
	require.Zero(t, r[0].Replayed)
	require.Contains(t, r[0].Stopped, "gap before nonce 2")
}

func TestBooksThatDoNotAddUpAreAViolation(t *testing.T) {
	ok := BalancePoint{Nonce: 3, Amount: 100, Balance: 60, Fees: 10, Charged: 20, Reserved: 10, Missed: 1, Invalid: 1}
	bad := BalancePoint{Nonce: 4, Amount: 100, Balance: 70, Fees: 10, Charged: 20, Reserved: 10, Missed: 1, Invalid: 1}
	in := PromiseInput{Balances: []BalanceReplay{
		{Host: "h1", Escrow: "7", Replayed: 2, Points: []BalancePoint{ok, bad}},
		{Host: "h2", Escrow: "7", Replayed: 1, Points: []BalancePoint{ok}},
	}}
	rep := PromiseReport{Checked: map[string]Measure{}}
	checkBalance(in, &rep)
	require.Equal(t, Measure{Measured: true, Judged: 2,
		Note: "2 escrow chains replayed across hosts, books checked after 3 diffs; 0 replays stopped early; refunds taken: 2 timeouts, 2 invalidations"}, rep.Checked[EscrowMoneyConserved])
	require.Empty(t, rep.Observations, "both refund paths were taken")
	require.Len(t, rep.Violations, 1)
	v := rep.Violations[0]
	require.Equal(t, EscrowMoneyConserved, v.Promise)
	require.Equal(t, "h1", v.Host)
	require.Nil(t, v.Inference, "a books violation is the escrow's, not an inference's")
	require.Contains(t, v.Detail, "escrow 7 nonce 4")
	require.Contains(t, v.Detail, "off by 10")
}

func TestBalanceNotReplayedIsNotMeasured(t *testing.T) {
	rep := PromiseReport{Checked: map[string]Measure{}}
	checkBalance(PromiseInput{}, &rep)
	require.False(t, rep.Checked[EscrowMoneyConserved].Measured)

	rep = PromiseReport{Checked: map[string]Measure{}}
	checkBalance(PromiseInput{Balances: []BalanceReplay{{Host: "h", Escrow: "1", Stopped: "no session row for the escrow"}}}, &rep)
	require.False(t, rep.Checked[EscrowMoneyConserved].Measured)
	require.Contains(t, rep.Checked[EscrowMoneyConserved].Note, "1 stopped")
}

// Test flow:
//  1. ESCROW_REPLAY_EXPORT names the export/ directories of finished load-test runs (a list).
//  2. Replay every escrow of every host through the state machine.
//  3. Check every chain replays to its last diff and the books add up after every diff.
func TestReplayBalancesOnARunExport(t *testing.T) {
	dirs := filepath.SplitList(os.Getenv("ESCROW_REPLAY_EXPORT"))
	if len(dirs) == 0 {
		t.Skip("set ESCROW_REPLAY_EXPORT to the export/ directories of finished runs")
	}
	for _, dir := range dirs {
		r, err := ReplayBalances(dir)
		require.NoError(t, err, dir)
		require.NotEmpty(t, r, dir)
		for _, b := range r {
			require.Empty(t, b.Stopped, "%s: %s/%s stopped after %d diffs", dir, b.Host, b.Escrow, b.Replayed)
			for _, p := range b.Points {
				require.Equal(t, p.Amount, p.Sum(), "%s: %s/%s nonce %d", dir, b.Host, b.Escrow, p.Nonce)
			}
		}
		rep := PromiseReport{Checked: map[string]Measure{}}
		checkBalance(PromiseInput{Balances: r}, &rep)
		t.Logf("%s: %+v", dir, rep.Checked[EscrowMoneyConserved])
	}
}

// Test flow:
//  1. The fixture's session row with the deposit one coin larger: every signature still verifies,
//     the state the diffs build does not match the roots the host computed.
//  2. Replay it.
//  3. Check the replay stops at the first diff on the root, not on a signature.
func TestReplayStopsWhereTheBooksDisagreeWithTheHostsRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "postgres")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	raw, err := os.ReadFile(filepath.Join(escrowReplayFixture, "postgres", "public.devshard_sessions.csv"))
	require.NoError(t, err)
	changed := strings.Replace(string(raw), ",5000000000,", ",5000000001,", 1)
	require.NotEqual(t, string(raw), changed)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "public.devshard_sessions.csv"), []byte(changed), 0o644))
	diffs, err := os.ReadFile(filepath.Join(escrowReplayFixture, "postgres", "public.devshard_diffs.csv"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "public.devshard_diffs.csv"), diffs, 0o644))
	r, err := ReplayBalances(filepath.Dir(dir))
	require.NoError(t, err)
	require.Zero(t, r[0].Replayed)
	require.Contains(t, r[0].Stopped, "nonce 1:")
	require.Contains(t, r[0].Stopped, "root")
	require.NotContains(t, r[0].Stopped, "signature")
}

// Test flow:
//  1. One escrow's books go off by 10 at nonce 2 and stay off.
//  2. Check the books.
//  3. Check it is one violation at the nonce where they went off, with how many diffs stayed off.
func TestBooksOffOnceIsOneViolation(t *testing.T) {
	ok := BalancePoint{Nonce: 1, Amount: 100, Balance: 60, Fees: 10, Charged: 20, Reserved: 10}
	off := func(n uint64) BalancePoint {
		return BalancePoint{Nonce: n, Amount: 100, Balance: 70, Fees: 10, Charged: 20, Reserved: 10}
	}
	rep := PromiseReport{Checked: map[string]Measure{}}
	checkBalance(PromiseInput{Balances: []BalanceReplay{{Host: "h1", Escrow: "7", Replayed: 4, Points: []BalancePoint{ok, off(2), off(3), off(4)}}}}, &rep)
	require.Len(t, rep.Violations, 1)
	require.Contains(t, rep.Violations[0].Detail, "nonce 2")
	require.Contains(t, rep.Violations[0].Detail, "off by 10 through nonce 4, 3 diffs")
}

// Test flow:
//  1. An export with one Postgres host and one SQLite host.
//  2. Replay the export and check the books.
//  3. Check the SQLite host is named as not replayed instead of silently left out.
func TestReplayNamesSQLiteHostsItDidNotReplay(t *testing.T) {
	export := t.TempDir()
	pg := filepath.Join(export, "postgres")
	require.NoError(t, os.MkdirAll(pg, 0o755))
	for _, f := range []string{"public.devshard_sessions.csv", "public.devshard_diffs.csv"} {
		raw, err := os.ReadFile(filepath.Join(escrowReplayFixture, "postgres", f))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(pg, f), raw, 0o644))
	}
	svc := filepath.Join(export, "sqlite", "versiond-1")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	db, err := sql.Open("sqlite", "file:"+filepath.Join(svc, "devshard.db"))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE diffs (escrow_id TEXT, nonce INTEGER)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	r, err := ReplayBalances(export)
	require.NoError(t, err)
	rep := PromiseReport{Checked: map[string]Measure{}}
	checkBalance(PromiseInput{Balances: r}, &rep)
	require.True(t, rep.Checked[EscrowMoneyConserved].Measured)
	require.Contains(t, strings.Join(rep.Observations, "\n"), "versiond-1")
}

// Test flow:
//  1. An export whose host holds its sessions but no diff yet.
//  2. Replay it and check the books.
//  3. Check the note says no chain was replayed, not that the export was never replayed.
func TestReplayOfAnExportWithoutDiffs(t *testing.T) {
	export := t.TempDir()
	pg := filepath.Join(export, "postgres")
	require.NoError(t, os.MkdirAll(pg, 0o755))
	raw, err := os.ReadFile(filepath.Join(escrowReplayFixture, "postgres", "public.devshard_sessions.csv"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(pg, "public.devshard_sessions.csv"), raw, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pg, "public.devshard_diffs.csv"), []byte("escrow_id,nonce,txs_proto,user_sig,post_state_root,warm_keys_json\n"), 0o644))
	r, err := ReplayBalances(export)
	require.NoError(t, err)
	rep := PromiseReport{Checked: map[string]Measure{}}
	checkBalance(PromiseInput{Balances: r}, &rep)
	require.Contains(t, rep.Checked[EscrowMoneyConserved].Note, "no escrow chain replayed")
}

// Test flow:
//  1. The fixture's session row with its version left empty.
//  2. Replay it.
//  3. Check the replay stops before the first diff and says the row named no version: the host
//     replayed on the version it bound, which the export does not hold.
func TestReplayOfASessionWithoutAVersion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "postgres")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	f, err := os.Open(filepath.Join(escrowReplayFixture, "postgres", "public.devshard_sessions.csv"))
	require.NoError(t, err)
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	require.NoError(t, err)
	rows[1][column(rows[0], "version")] = ""
	out, err := os.Create(filepath.Join(dir, "public.devshard_sessions.csv"))
	require.NoError(t, err)
	require.NoError(t, csv.NewWriter(out).WriteAll(rows))
	require.NoError(t, out.Close())
	diffs, err := os.ReadFile(filepath.Join(escrowReplayFixture, "postgres", "public.devshard_diffs.csv"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "public.devshard_diffs.csv"), diffs, 0o644))
	r, err := ReplayBalances(filepath.Dir(dir))
	require.NoError(t, err)
	require.Zero(t, r[0].Replayed)
	require.Equal(t, "the session row names no version", r[0].Stopped)
}
