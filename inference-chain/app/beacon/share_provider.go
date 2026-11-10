// Package beacon holds node-side pieces of the per-block BLS beacon that do not
// depend on keepers (Step 2+ of
// devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md).
package beacon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cosmossdk.io/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/fsnotify/fsnotify"
	blst "github.com/supranational/blst/bindings/go"

	"github.com/productscience/inference/app/beacon/sharefile"
)

const (
	// DefaultPollInterval is the polling fallback when fsnotify misses an event
	// (bind mounts on some platforms do not deliver inotify events).
	DefaultPollInterval = 30 * time.Second
	// verifyRetryBlocks: when committed state is not ready to check an epoch's
	// shares (DKG not final yet), retry after this many heights.
	verifyRetryBlocks = 20
	// reloadDebounce coalesces the burst of events one atomic write produces.
	reloadDebounce = 200 * time.Millisecond
)

// EpochShares are one participant's verified secret shares for an epoch.
// Values returned by a ShareProvider are shared and must not be modified.
type EpochShares struct {
	EpochID            uint64
	ChainID            string
	ParticipantAddress string
	SlotStart          uint32
	SlotEnd            uint32
	GroupPublicKey     []byte
	// Scalars are the slot secret keys, little-endian (blst scalar order),
	// Scalars[i] belongs to slot SlotStart+i.
	Scalars [][sharefile.ScalarLen]byte
}

// NumSlots returns the number of slots covered.
func (s *EpochShares) NumSlots() int { return len(s.Scalars) }

// ShareProvider returns this node's verified BLS slot shares for an epoch.
// Get is called from ABCI handlers: it must never block on I/O or the network
// and must never panic.
type ShareProvider interface {
	Get(ctx sdk.Context, epochID uint64) (*EpochShares, bool)
}

// EpochKeys is what committed state says about an epoch's key.
type EpochKeys struct {
	// Ready is false while the epoch's DKG is not final (retry later).
	Ready          bool
	GroupPublicKey []byte
	// SlotPublicKeys[i] is the compressed G2 public key of slot SlotStart+i.
	SlotPublicKeys [][]byte
	// OwnerAddress is the participant that owns [SlotStart, SlotEnd] in this
	// epoch, or "" if no participant owns exactly that range.
	OwnerAddress string
}

// CommittedKeys reads an epoch's committed key material. It is implemented in
// package app on top of the x/bls keeper.
type CommittedKeys interface {
	EpochKeys(ctx sdk.Context, epochID uint64, slotStart, slotEnd uint32) (EpochKeys, error)
}

// CommittedKeysFunc adapts a function to CommittedKeys.
type CommittedKeysFunc func(ctx sdk.Context, epochID uint64, slotStart, slotEnd uint32) (EpochKeys, error)

// EpochKeys implements CommittedKeys.
func (f CommittedKeysFunc) EpochKeys(ctx sdk.Context, epochID uint64, slotStart, slotEnd uint32) (EpochKeys, error) {
	return f(ctx, epochID, slotStart, slotEnd)
}

type verifyState int

const (
	stateUnverified verifyState = iota
	stateValid
	stateInvalid
)

func (s verifyState) String() string {
	switch s {
	case stateValid:
		return "valid"
	case stateInvalid:
		return "invalid"
	default:
		return "unverified"
	}
}

// fileStamp identifies a file version cheaply. The mode is included so fixing
// a rejected file's permissions is noticed.
type fileStamp struct {
	size    int64
	modTime time.Time
	mode    os.FileMode
}

type shareEntry struct {
	shares        *EpochShares
	checksum      string
	stamp         fileStamp
	state         verifyState
	reason        string
	retryAtHeight int64
}

// FileShareProvider serves shares exported by the dAPI into a directory (see
// package sharefile). Files are loaded into memory at construction and on
// change (fsnotify plus polling); Get never touches the disk.
//
// A file is checked against committed state once, on the first Get for its
// epoch: the group public key, slot ownership and sk_i·G2 == slotPublicKey_i
// for every slot. A file that fails stays rejected until it changes on disk.
type FileShareProvider struct {
	dir          string
	logger       log.Logger
	keys         CommittedKeys
	pollInterval time.Duration

	mu       sync.RWMutex
	entries  map[uint64]*shareEntry
	rejected map[string]fileStamp // file name -> stamp of the last rejected version (log once)
	floor    uint64               // epochs below are ignored (older than current-1)

	verifyMu sync.Mutex // one verification at a time

	stopOnce sync.Once
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewFileShareProvider creates the directory if needed (mode 0700), loads
// existing files and returns the provider. Call Start to watch for changes.
func NewFileShareProvider(dir string, keys CommittedKeys, logger log.Logger) (*FileShareProvider, error) {
	if dir == "" {
		return nil, errors.New("beacon: empty share dir")
	}
	if keys == nil {
		return nil, errors.New("beacon: nil CommittedKeys")
	}
	if logger == nil {
		logger = log.NewNopLogger()
	}
	if err := sharefile.EnsureDir(dir); err != nil {
		return nil, fmt.Errorf("beacon: share dir %s: %w", dir, err)
	}
	p := &FileShareProvider{
		dir:          dir,
		logger:       logger, // the caller tags it (the app passes module=beacon)
		keys:         keys,
		pollInterval: DefaultPollInterval,
		entries:      make(map[uint64]*shareEntry),
		rejected:     make(map[string]fileStamp),
	}
	p.Reload()
	return p, nil
}

// Dir returns the watched directory.
func (p *FileShareProvider) Dir() string { return p.dir }

// SetPollInterval changes the polling fallback; call before Start.
func (p *FileShareProvider) SetPollInterval(d time.Duration) {
	if d > 0 {
		p.pollInterval = d
	}
}

// Start watches the directory until ctx is done or Close is called.
func (p *FileShareProvider) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.done = make(chan struct{})
	go p.run(ctx)
}

// Close stops the watcher and waits for it to exit.
func (p *FileShareProvider) Close() error {
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
			<-p.done
		}
	})
	return nil
}

func (p *FileShareProvider) run(ctx context.Context) {
	defer close(p.done)

	var events <-chan fsnotify.Event
	var errs <-chan error
	watcher, err := fsnotify.NewWatcher()
	if err == nil {
		if err = watcher.Add(p.dir); err == nil {
			events, errs = watcher.Events, watcher.Errors
			defer watcher.Close()
		} else {
			watcher.Close()
		}
	}
	if err != nil {
		p.logger.Warn("beacon share watcher unavailable, polling only", "dir", p.dir, "interval", p.pollInterval, "err", err)
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if _, isShare := sharefile.ParseFileName(filepath.Base(ev.Name)); isShare && debounce == nil {
				debounce = time.After(reloadDebounce)
			}
		case werr, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			p.logger.Warn("beacon share watcher error", "err", werr)
		case <-debounce:
			debounce = nil
			p.safeReload()
		case <-ticker.C:
			p.safeReload()
		}
	}
}

func (p *FileShareProvider) safeReload() {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("beacon share reload panicked", "panic", r)
		}
	}()
	p.Reload()
}

// Reload rescans the directory. Unchanged files keep their verification
// state; changed files are re-read and must be verified again.
func (p *FileShareProvider) Reload() {
	dirEntries, err := os.ReadDir(p.dir)
	if err != nil {
		p.logger.Warn("beacon share dir unreadable", "dir", p.dir, "err", err)
		return
	}

	type found struct {
		name  string
		stamp fileStamp
	}
	seen := make(map[uint64]found)
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		epochID, ok := sharefile.ParseFileName(de.Name())
		if !ok {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		seen[epochID] = found{name: de.Name(), stamp: fileStamp{size: info.Size(), modTime: info.ModTime(), mode: info.Mode()}}
	}

	p.mu.Lock()
	floor := p.floor
	// Forget entries whose file is gone or that fell below the floor.
	for epochID := range p.entries {
		if _, ok := seen[epochID]; !ok || epochID < floor {
			delete(p.entries, epochID)
			p.logger.Info("beacon shares unloaded", "epoch", epochID)
		}
	}
	// Collect what needs (re)reading without holding the lock during I/O.
	var toRead []uint64
	for epochID, f := range seen {
		if epochID < floor {
			continue
		}
		if e, ok := p.entries[epochID]; ok && e.stamp == f.stamp {
			continue
		}
		if st, ok := p.rejected[f.name]; ok && st == f.stamp {
			continue
		}
		toRead = append(toRead, epochID)
	}
	p.mu.Unlock()

	for _, epochID := range toRead {
		f := seen[epochID]
		path := filepath.Join(p.dir, f.name)
		file, err := sharefile.ReadFile(path, "")
		if err == nil && file.EpochID != epochID {
			err = fmt.Errorf("file name says epoch %d, content says %d", epochID, file.EpochID)
		}
		if err == nil {
			err = checkMode(path)
		}

		p.mu.Lock()
		if err != nil {
			p.rejected[f.name] = f.stamp
			delete(p.entries, epochID)
			p.mu.Unlock()
			p.logger.Warn("beacon share file rejected", "file", f.name, "err", err)
			continue
		}
		delete(p.rejected, f.name)
		if e, ok := p.entries[epochID]; ok && e.checksum == file.SHA256 {
			e.stamp = f.stamp // touched, same content
			p.mu.Unlock()
			continue
		}
		p.entries[epochID] = &shareEntry{
			shares:   toEpochShares(file),
			checksum: file.SHA256,
			stamp:    f.stamp,
			state:    stateUnverified,
		}
		p.mu.Unlock()
		p.logger.Info("beacon share file read", "epoch", epochID,
			"slots", fmt.Sprintf("[%d,%d]", file.SlotStartIndex, file.SlotEndIndex))
	}
}

// checkMode rejects share files readable by group or others: they hold secret
// keys, and a loose mode means something other than the exporter wrote them.
func checkMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("mode %#o is too open (want %#o)", info.Mode().Perm(), sharefile.FileMode)
	}
	return nil
}

func toEpochShares(f *sharefile.File) *EpochShares {
	be := f.SharesBigEndian()
	scalars := make([][sharefile.ScalarLen]byte, len(be))
	for i := range be {
		for j := 0; j < sharefile.ScalarLen; j++ {
			scalars[i][j] = be[i][sharefile.ScalarLen-1-j]
		}
	}
	return &EpochShares{
		EpochID:            f.EpochID,
		ChainID:            f.ChainID,
		ParticipantAddress: f.ParticipantAddress,
		SlotStart:          f.SlotStartIndex,
		SlotEnd:            f.SlotEndIndex,
		GroupPublicKey:     f.GroupPublicKeyBytes(),
		Scalars:            scalars,
	}
}

// Get returns verified shares for epochID. The first call for a newly loaded
// file verifies it against committed state in ctx. It never panics.
func (p *FileShareProvider) Get(ctx sdk.Context, epochID uint64) (shares *EpochShares, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("beacon share provider panicked", "epoch", epochID, "panic", r)
			shares, ok = nil, false
		}
	}()

	p.advanceFloor(epochID)

	p.mu.RLock()
	e, found := p.entries[epochID]
	var state verifyState
	var retryAt int64
	if found {
		state, retryAt = e.state, e.retryAtHeight
		shares = e.shares
	}
	p.mu.RUnlock()

	switch {
	case !found, state == stateInvalid:
		return nil, false
	case state == stateValid:
		if cid := ctx.ChainID(); cid != "" && cid != shares.ChainID {
			return nil, false
		}
		return shares, true
	case ctx.BlockHeight() < retryAt:
		return nil, false
	}
	return p.verify(ctx, epochID)
}

// advanceFloor drops entries older than epochID-1 (the plan keeps the current
// and the previous/next epoch only).
func (p *FileShareProvider) advanceFloor(epochID uint64) {
	if epochID == 0 {
		return
	}
	floor := epochID - 1
	p.mu.RLock()
	stale := floor > p.floor
	p.mu.RUnlock()
	if !stale {
		return
	}
	p.mu.Lock()
	if floor > p.floor {
		p.floor = floor
		for id := range p.entries {
			if id < floor {
				delete(p.entries, id)
			}
		}
	}
	p.mu.Unlock()
}

func (p *FileShareProvider) verify(ctx sdk.Context, epochID uint64) (*EpochShares, bool) {
	p.verifyMu.Lock()
	defer p.verifyMu.Unlock()

	p.mu.RLock()
	e, found := p.entries[epochID]
	if !found {
		p.mu.RUnlock()
		return nil, false
	}
	shares, checksum, state := e.shares, e.checksum, e.state
	p.mu.RUnlock()
	if state == stateValid {
		return shares, true
	}
	if state == stateInvalid {
		return nil, false
	}

	start := time.Now()
	newState, reason, retry := p.check(ctx, shares)

	p.mu.Lock()
	cur, still := p.entries[epochID]
	if still && cur.checksum == checksum {
		if retry {
			cur.retryAtHeight = ctx.BlockHeight() + verifyRetryBlocks
		} else {
			cur.state, cur.reason = newState, reason
		}
	}
	p.mu.Unlock()

	if retry {
		p.logger.Info("beacon shares not verifiable yet", "epoch", epochID, "reason", reason,
			"retry_at_height", ctx.BlockHeight()+verifyRetryBlocks)
		return nil, false
	}
	slots := fmt.Sprintf("[%d,%d]", shares.SlotStart, shares.SlotEnd)
	if newState != stateValid {
		p.logger.Error("beacon shares loaded", "epoch", epochID, "slots", slots, "valid", false,
			"reason", reason, "took", time.Since(start).String())
		return nil, false
	}
	p.logger.Info("beacon shares loaded", "epoch", epochID, "slots", slots, "valid", true,
		"took", time.Since(start).String())
	return shares, true
}

// check returns the verification verdict. retry=true means committed state is
// not ready yet and the file must not be judged.
func (p *FileShareProvider) check(ctx sdk.Context, s *EpochShares) (state verifyState, reason string, retry bool) {
	if cid := ctx.ChainID(); cid != "" && cid != s.ChainID {
		return stateInvalid, fmt.Sprintf("chain_id %q, node is on %q", s.ChainID, cid), false
	}
	keys, err := p.keys.EpochKeys(ctx, s.EpochID, s.SlotStart, s.SlotEnd)
	if err != nil {
		return stateInvalid, "committed keys: " + err.Error(), false
	}
	if !keys.Ready {
		return stateUnverified, "epoch DKG not final", true
	}
	if !bytes.Equal(keys.GroupPublicKey, s.GroupPublicKey) {
		return stateInvalid, "group_public_key does not match committed state", false
	}
	if keys.OwnerAddress != s.ParticipantAddress {
		return stateInvalid, fmt.Sprintf("slot range owned by %q, file says %q", keys.OwnerAddress, s.ParticipantAddress), false
	}
	if len(keys.SlotPublicKeys) != s.NumSlots() {
		return stateInvalid, fmt.Sprintf("%d slot public keys for %d shares", len(keys.SlotPublicKeys), s.NumSlots()), false
	}
	for i := range s.Scalars {
		if err := VerifyShare(s.Scalars[i], keys.SlotPublicKeys[i]); err != nil {
			return stateInvalid, fmt.Sprintf("slot %d: %v", s.SlotStart+uint32(i), err), false
		}
	}
	return stateValid, "", false
}

// VerifyShare checks sk·G2 == slotPublicKey (compressed G2) for a little-endian scalar.
func VerifyShare(scalarLE [sharefile.ScalarLen]byte, slotPublicKey []byte) error {
	want := new(blst.P2Affine).Uncompress(slotPublicKey)
	if want == nil {
		return errors.New("invalid slot public key")
	}
	got := blst.P2Generator().Mult(scalarLE[:], 255).ToAffine()
	if !got.Equals(want) {
		return errors.New("share does not match slot public key")
	}
	return nil
}

// Status is a snapshot of one loaded epoch, for logs and tests.
type Status struct {
	EpochID   uint64
	SlotStart uint32
	SlotEnd   uint32
	State     string
	Reason    string
}

// Statuses returns the loaded epochs and their verification state.
func (p *FileShareProvider) Statuses() []Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Status, 0, len(p.entries))
	for id, e := range p.entries {
		out = append(out, Status{EpochID: id, SlotStart: e.shares.SlotStart, SlotEnd: e.shares.SlotEnd, State: e.state.String(), Reason: e.reason})
	}
	return out
}
