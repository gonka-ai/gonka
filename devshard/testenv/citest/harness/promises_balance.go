package harness

// escrow_money_conserved: the money of an escrow is conserved. Each host's diff chain is replayed through the real state
// machine from the session the host bound (config, group, amount, creator), in nonce order. The
// state machine checks the user signature of every diff and its post_state_root when the diff
// carries one, so a replay that goes through reproduces what the host computed. After every diff the escrow's amount has to equal
//
//	balance + fees + cost charged to executors + reserve held by inferences still Pending/Started
//
// Executors' charged cost is read from host stats, not from the inference records, because sealed
// inferences leave the record map while their cost stays charged.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
)

// BalancePoint is the escrow's books after one diff of the replay.
type BalancePoint struct {
	Nonce    uint64 `json:"nonce"`
	Amount   uint64 `json:"amount"`
	Balance  uint64 `json:"balance"`
	Fees     uint64 `json:"fees"`
	Charged  uint64 `json:"charged"`
	Reserved uint64 `json:"reserved"`
	// Missed and Invalid count the refund paths taken so far: a timeout returns the reserve, an
	// invalidation takes the charged cost back from the executor.
	Missed  uint64 `json:"missed"`
	Invalid uint64 `json:"invalid"`
}

// Sum is everything the books account for.
func (p BalancePoint) Sum() uint64 { return p.Balance + p.Fees + p.Charged + p.Reserved }

// BalanceReplay is the replay of one escrow on one host.
type BalanceReplay struct {
	Host     string `json:"host"`
	Escrow   string `json:"escrow"`
	Replayed int    `json:"replayed"`
	// Stopped says why the replay ended before the last diff of the chain; empty when it reached it.
	Stopped string `json:"stopped,omitempty"`
	// Points are the books after every replayed diff, in nonce order.
	Points []BalancePoint `json:"-"`
}

type sessionRow struct {
	escrow, creator, version string
	epoch, amount            uint64
	config                   types.SessionConfig
	group                    []types.SlotAssignment
}

func readSessions(dbDir string) (map[string]sessionRow, error) {
	rows, err := readCSV(filepath.Join(dbDir, "public.devshard_sessions.csv"))
	if err != nil {
		return nil, err
	}
	out := map[string]sessionRow{}
	for _, r := range rows {
		s := sessionRow{escrow: r["escrow_id"], creator: r["creator_addr"], version: r["version"]}
		if v := r["epoch_id"]; v != "" {
			if s.epoch, err = strconv.ParseUint(v, 10, 64); err != nil {
				return nil, fmt.Errorf("escrow %s epoch_id %q: %w", s.escrow, v, err)
			}
		}
		if s.amount, err = strconv.ParseUint(r["initial_balance"], 10, 64); err != nil {
			return nil, fmt.Errorf("escrow %s initial_balance %q: %w", s.escrow, r["initial_balance"], err)
		}
		if err := json.Unmarshal([]byte(r["config_json"]), &s.config); err != nil {
			return nil, fmt.Errorf("escrow %s config_json: %w", s.escrow, err)
		}
		if err := json.Unmarshal([]byte(r["group_json"]), &s.group); err != nil {
			return nil, fmt.Errorf("escrow %s group_json: %w", s.escrow, err)
		}
		out[s.escrow] = s
	}
	return out, nil
}

func unhex(v string) ([]byte, error) { return hex.DecodeString(strings.TrimPrefix(v, `\x`)) }

// ReplayBalances replays every escrow of every database in an export (postgres/ and db-*/postgres/).
// A host on SQLite (sqlite/<service>) is not replayed; it is listed with the reason.
func ReplayBalances(exportDir string) ([]BalanceReplay, error) {
	dirs, _ := filepath.Glob(filepath.Join(exportDir, "postgres"))
	more, _ := filepath.Glob(filepath.Join(exportDir, "db-*", "postgres"))
	dirs = append(dirs, more...)
	if len(dirs) == 0 {
		return nil, fmt.Errorf("no database under %s", exportDir)
	}
	// Without the chain's headers a Start stamped with the floor stops the replay: its time is pending.
	headers, err := ReadChainHeaders(exportDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	out := []BalanceReplay{}
	for _, dir := range dirs {
		host := "primary"
		if d := filepath.Base(filepath.Dir(dir)); strings.HasPrefix(d, "db-") {
			host = strings.TrimPrefix(d, "db-")
		}
		sessions, err := readSessions(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", host, err)
		}
		rows, err := readCSV(filepath.Join(dir, "public.devshard_diffs.csv"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", host, err)
		}
		byEscrow := map[string][]map[string]string{}
		for _, r := range rows {
			byEscrow[r["escrow_id"]] = append(byEscrow[r["escrow_id"]], r)
		}
		escrows := make([]string, 0, len(byEscrow))
		for e := range byEscrow {
			escrows = append(escrows, e)
		}
		sort.Strings(escrows)
		for _, e := range escrows {
			out = append(out, replayEscrow(host, sessions[e], e, byEscrow[e], headers))
		}
	}
	services, _ := filepath.Glob(filepath.Join(exportDir, "sqlite", "*"))
	for _, dir := range services {
		if len(sqliteFilesIn(dir)) > 0 {
			out = append(out, BalanceReplay{Host: filepath.Base(dir), Stopped: "a SQLite host: the replay reads Postgres exports only"})
		}
	}
	return out, nil
}

func replayEscrow(host string, s sessionRow, escrow string, rows []map[string]string, headers ChainHeaders) BalanceReplay {
	rep := BalanceReplay{Host: host, Escrow: escrow}
	if s.escrow == "" {
		rep.Stopped = "no session row for the escrow"
		return rep
	}
	if s.version == "" {
		// The host fell back to the version it bound the session with, which the export does not hold.
		rep.Stopped = "the session row names no version"
		return rep
	}
	type diffRow struct {
		nonce uint64
		row   map[string]string
	}
	diffs := make([]diffRow, 0, len(rows))
	for _, r := range rows {
		n, err := strconv.ParseUint(r["nonce"], 10, 64)
		if err != nil {
			rep.Stopped = fmt.Sprintf("nonce %q: %v", r["nonce"], err)
			return rep
		}
		diffs = append(diffs, diffRow{n, r})
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].nonce < diffs[j].nonce })

	// Warm key bindings the chain approved are not in the export; the diffs record the bindings
	// they introduced, and those are the ones a replay can honour.
	bound := map[string]bool{}
	slotAddr := map[uint32]string{}
	for _, g := range s.group {
		slotAddr[g.SlotID] = g.ValidatorAddress
	}
	for _, d := range diffs {
		var keys map[uint32]string
		if v := d.row["warm_keys_json"]; v != "" && v != "null" && json.Unmarshal([]byte(v), &keys) == nil {
			for slot, warm := range keys {
				bound[warm+"/"+slotAddr[slot]] = true
			}
		}
	}
	resolver := func(warm, cold string) (bool, error) { return bound[warm+"/"+cold], nil }

	// The state machine writes every sealed inference to the store under the session, and the drain
	// at settlement fails without it, so the replay's store holds the session the host bound.
	store := storage.NewMemory()
	if err := store.CreateSession(storage.CreateSessionParams{EscrowID: escrow, EpochID: s.epoch, Version: s.version,
		CreatorAddr: s.creator, Config: s.config, Group: s.group, InitialBalance: s.amount}); err != nil {
		rep.Stopped = "create session: " + err.Error()
		return rep
	}
	opts := []state.SMOption{state.WithWarmKeyResolver(resolver), state.WithVersion(s.version)}
	if headers != nil {
		opts = append(opts, state.WithHeaderTimes(headers))
	}
	sm, err := state.NewStateMachine(escrow, s.config, s.group, s.amount, s.creator,
		signing.NewSecp256k1Verifier(), store, opts...)
	if err != nil {
		rep.Stopped = "create state machine: " + err.Error()
		return rep
	}
	for i, d := range diffs {
		if d.nonce != uint64(i)+1 {
			rep.Stopped = fmt.Sprintf("chain starts or has a gap before nonce %d", d.nonce)
			return rep
		}
		raw, err := unhex(d.row["txs_proto"])
		if err != nil {
			rep.Stopped = fmt.Sprintf("nonce %d txs_proto: %v", d.nonce, err)
			return rep
		}
		var dc types.DiffContent
		if err := proto.Unmarshal(raw, &dc); err != nil {
			rep.Stopped = fmt.Sprintf("nonce %d txs_proto: %v", d.nonce, err)
			return rep
		}
		sig, err1 := unhex(d.row["user_sig"])
		root, err2 := unhex(d.row["post_state_root"])
		if err1 != nil || err2 != nil {
			rep.Stopped = fmt.Sprintf("nonce %d: bad user_sig or post_state_root", d.nonce)
			return rep
		}
		if _, err := sm.ApplyDiff(types.Diff{Nonce: d.nonce, Txs: dc.Txs, UserSig: sig, PostStateRoot: root}); err != nil {
			rep.Stopped = fmt.Sprintf("nonce %d: %v", d.nonce, err)
			return rep
		}
		rep.Replayed++
		rep.Points = append(rep.Points, books(sm, s.amount))
	}
	return rep
}

func books(sm *state.StateMachine, amount uint64) BalancePoint {
	st := sm.SnapshotState()
	p := BalancePoint{Nonce: st.LatestNonce, Amount: amount, Balance: st.Balance, Fees: st.Fees}
	for _, hs := range st.HostStats {
		p.Charged += hs.Cost
		p.Missed += uint64(hs.Missed)
		p.Invalid += uint64(hs.Invalid)
	}
	for _, rec := range st.Inferences {
		if rec.Status == types.StatusPending || rec.Status == types.StatusStarted {
			p.Reserved += rec.ReservedCost
		}
	}
	return p
}

// checkBalance holds every replayed chain to escrow_money_conserved.
func checkBalance(in PromiseInput, rep *PromiseReport) {
	if in.Balances == nil {
		rep.Checked[EscrowMoneyConserved] = notMeasured("the export was not replayed through the state machine")
		return
	}
	judged, points, stopped := 0, 0, []string{}
	var missed, invalid uint64
	for _, b := range in.Balances {
		switch {
		case b.Stopped != "" && b.Escrow == "":
			stopped = append(stopped, fmt.Sprintf("%s: %s", b.Host, b.Stopped))
		case b.Stopped != "":
			stopped = append(stopped, fmt.Sprintf("%s/%s after %d diffs: %s", b.Host, b.Escrow, b.Replayed, b.Stopped))
		}
		if b.Replayed == 0 {
			continue
		}
		judged++
		points += b.Replayed
		last := b.Points[len(b.Points)-1]
		missed += last.Missed
		invalid += last.Invalid
		rep.Violations = append(rep.Violations, booksOff(b)...)
	}
	for _, s := range stopped {
		rep.Observations = append(rep.Observations, EscrowMoneyConserved+" replay stopped: "+s)
	}
	if judged == 0 {
		rep.Checked[EscrowMoneyConserved] = notMeasured(fmt.Sprintf("no escrow chain replayed (%d stopped)", len(stopped)))
		return
	}
	if missed == 0 || invalid == 0 {
		rep.Observations = append(rep.Observations, fmt.Sprintf(
			EscrowMoneyConserved+" checked only the paths the replay reached: %d timeout refunds, %d invalidations across host chains", missed, invalid))
	}
	rep.Checked[EscrowMoneyConserved] = measured(judged, "%d escrow chains replayed across hosts, books checked after %d diffs; %d replays stopped early; refunds taken: %d timeouts, %d invalidations",
		judged, points, len(stopped), missed, invalid)
}

// booksOff reports where one chain's books stop adding up: one violation each time the offset
// changes, at the first nonce of the run of diffs it holds over, since a divergence carries into
// every later diff.
func booksOff(b BalanceReplay) []PromiseViolation {
	var out []PromiseViolation
	flush := func(first, last BalancePoint, n int) {
		off := int64(first.Sum()) - int64(first.Amount)
		if off == 0 {
			return
		}
		out = append(out, PromiseViolation{Promise: EscrowMoneyConserved, Host: b.Host,
			Detail: fmt.Sprintf("escrow %s nonce %d: amount %d != balance %d + fees %d + charged %d + reserved %d (off by %d through nonce %d, %d diffs)",
				b.Escrow, first.Nonce, first.Amount, first.Balance, first.Fees, first.Charged, first.Reserved, off, last.Nonce, n)})
	}
	for i := 0; i < len(b.Points); {
		j, off := i, int64(b.Points[i].Sum())-int64(b.Points[i].Amount)
		for j+1 < len(b.Points) && int64(b.Points[j+1].Sum())-int64(b.Points[j+1].Amount) == off {
			j++
		}
		flush(b.Points[i], b.Points[j], j-i+1)
		i = j + 1
	}
	return out
}
