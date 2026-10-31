package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
)

// suspiciousHosts is the operator's pin list, written to the store first. See ../README.md, "Admin operations".
type suspiciousHosts struct {
	pins suspiciousHostStore

	mu    sync.RWMutex
	hosts map[string]bool
}
type suspiciousHostStore interface {
	ListSuspiciousHosts(ctx context.Context) ([]string, error)
	AddSuspiciousHost(ctx context.Context, participantKey string) error
	RemoveSuspiciousHost(ctx context.Context, participantKey string) error
}

func newSuspiciousHosts(ctx context.Context, pins suspiciousHostStore) (*suspiciousHosts, error) {
	pinned, err := pins.ListSuspiciousHosts(ctx)
	if err != nil {
		return nil, err
	}
	hosts := make(map[string]bool, len(pinned))
	for _, participantKey := range pinned {
		hosts[participantKey] = true
	}
	return &suspiciousHosts{pins: pins, hosts: hosts}, nil
}

func (s *suspiciousHosts) Suspicious(participantKey string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hosts[participantKey]
}

func (s *suspiciousHosts) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	listed := make([]string, 0, len(s.hosts))
	for host := range s.hosts {
		listed = append(listed, host)
	}
	sort.Strings(listed)
	return listed
}

func (s *suspiciousHosts) Add(ctx context.Context, participantKey string) error {
	if err := s.pins.AddSuspiciousHost(ctx, participantKey); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[participantKey] = true
	return nil
}

func (s *suspiciousHosts) Remove(ctx context.Context, participantKey string) error {
	if err := s.pins.RemoveSuspiciousHost(ctx, participantKey); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hosts, participantKey)
	return nil
}

// escrowLifecycle is the chain-facing half of an operator action; *escrow.Manager satisfies it.
type escrowLifecycle interface {
	CreateEscrow(ctx context.Context, model escrow.ModelConfig) (chain.CreateEscrowResult, error)
	Settle(ctx context.Context, escrowID string, force bool) (chain.SettleEscrowResult, error)
}
type operations struct {
	values       env.Values
	config       *config.Holder
	store        *store.Store
	escrows      *registry.Registry
	manager      escrowLifecycle
	participants *limits.ParticipantLimiter
	storageDir   string
	nonces       *nonces.Recorder

	writes sync.Map
}

// CreateEscrow takes the name of the variable holding the signing key, never the key. See operations.md, "What is exposed".
func (o *operations) CreateEscrow(ctx context.Context, request api.CreateEscrowRequest) (chain.CreateEscrowResult, error) {
	if strings.TrimSpace(request.PrivateKeyEnv) == "" {
		return chain.CreateEscrowResult{}, api.ErrPrivateKeyEnvRequired
	}
	result, err := o.manager.CreateEscrow(ctx, escrow.ModelConfig{
		ModelID:       request.Model,
		Amount:        request.Amount,
		PrivateKeyEnv: request.PrivateKeyEnv,
	})
	if err != nil {
		return chain.CreateEscrowResult{}, err
	}
	if !request.Activate {
		return result, o.Deactivate(ctx, escrowID(result.EscrowID))
	}
	return result, o.escrows.Add(ctx, escrowID(result.EscrowID), request.Model)
}

func (o *operations) AddDevshard(ctx context.Context, request api.AddDevshardRequest) error {
	return o.register(ctx, store.DevshardRecord{
		EscrowID:      request.EscrowID,
		PrivateKeyEnv: request.PrivateKeyEnv,
		Model:         request.Model,
		Active:        request.Activate,
	})
}

// ImportDevshard copies the storage, so the gateway owns the only handle to what it serves; every refusal but a park landing during the copy comes before it.
func (o *operations) ImportDevshard(ctx context.Context, request api.ImportDevshardRequest) error {
	record := store.DevshardRecord{
		EscrowID:      request.EscrowID,
		PrivateKeyEnv: request.PrivateKeyEnv,
		Model:         request.Model,
		Active:        request.Activate,
	}
	if err := refuseUnregistrable(record); err != nil {
		return err
	}
	defer o.lockWrites(record.EscrowID)()
	existing, err := findDevshard(ctx, o.store, record.EscrowID)
	if err != nil && !errors.Is(err, escrow.ErrUnknownEscrow) {
		return err
	}
	if err := refuseParkedRecord(existing); err != nil {
		return err
	}
	if err := o.refuseServedStorage(record.EscrowID, existing.Active); err != nil {
		return err
	}
	storagePath, err := escrowStorage(o.storageDir, request.EscrowID)
	if err != nil {
		return err
	}
	if err := copySessionStorage(request.SourcePath, storagePath); err != nil {
		return err
	}
	return o.save(ctx, record)
}

// DeleteDevshard refuses a parked escrow, whose storage its settlement still needs, and one whose row is active or whose session the registry still holds.
func (o *operations) DeleteDevshard(ctx context.Context, id string) error {
	defer o.lockWrites(id)()
	record, err := findDevshard(ctx, o.store, id)
	if err != nil {
		return unknownDevshard(id, err)
	}
	if isParked(record) {
		return parkedError(escrow.ErrDevshardBusy, id)
	}
	if record.Active || o.escrows.HoldsSession(id) {
		return fmt.Errorf("%w: %s", escrow.ErrDevshardBusy, id)
	}
	if err := o.store.DeleteDevshard(ctx, id); err != nil {
		return unknownDevshard(id, err)
	}
	return removeDevshardStorage(api.DevshardStoragePath(o.storageDir, id), o.storageDir)
}

func (o *operations) register(ctx context.Context, record store.DevshardRecord) error {
	if err := refuseUnregistrable(record); err != nil {
		return err
	}
	defer o.lockWrites(record.EscrowID)()
	return o.save(ctx, record)
}

func (o *operations) lockWrites(escrowID string) func() {
	lock, _ := o.writes.LoadOrStore(escrowID, &sync.Mutex{})
	writes := lock.(*sync.Mutex)
	writes.Lock()
	return writes.Unlock
}

func refuseUnregistrable(record store.DevshardRecord) error {
	if !isChainEscrowID(record.EscrowID) {
		return fmt.Errorf("%w: %q", api.ErrInvalidEscrowID, record.EscrowID)
	}
	_, err := env.PrivateKey(record.PrivateKeyEnv)
	return err
}

func (o *operations) save(ctx context.Context, record store.DevshardRecord) error {
	if !record.Active {
		if err := o.escrows.Retire(record.EscrowID); err != nil {
			return err
		}
	}
	listing := o.escrows.Listing()
	defer listing.Close()
	written, err := o.store.UpsertDevshardUnlessParked(ctx, record)
	if err != nil {
		return err
	}
	if !written {
		return parkedError(api.ErrDevshardNotActivatable, record.EscrowID)
	}
	if !record.Active {
		return o.escrows.Retire(record.EscrowID)
	}
	return addServing(ctx, listing, record)
}

func (o *operations) refuseServedStorage(escrowID string, active bool) error {
	if active || o.escrows.HoldsSession(escrowID) {
		return fmt.Errorf("%w: escrow %s is serving", api.ErrDevshardExists, escrowID)
	}
	return nil
}

// A parked escrow's balance is already committed: serving spends nonces the settlement misses.
func refuseParkedRecord(record store.DevshardRecord) error {
	if isParked(record) {
		return parkedError(api.ErrDevshardNotActivatable, record.EscrowID)
	}
	return nil
}

func unknownDevshard(escrowID string, err error) error {
	if errors.Is(err, escrow.ErrUnknownEscrow) || errors.Is(err, store.ErrDevshardNotFound) {
		return fmt.Errorf("%w: %s", api.ErrUnknownDevshard, escrowID)
	}
	return err
}

func isParked(record store.DevshardRecord) bool {
	return record.SettlementPending || record.SettleTxHash != ""
}

func parkedError(refusal error, escrowID string) error {
	return fmt.Errorf("%w: escrow %s is parked for settlement", refusal, escrowID)
}

func (o *operations) Activate(ctx context.Context, id string) error {
	defer o.lockWrites(id)()
	listing := o.escrows.Listing()
	defer listing.Close()
	activated, err := o.store.ActivateDevshardUnlessParked(ctx, id)
	if err != nil {
		return err
	}
	record, err := findDevshard(ctx, o.store, id)
	if err != nil {
		return unknownDevshard(id, err)
	}
	if !activated {
		return parkedError(api.ErrDevshardNotActivatable, id)
	}
	return addServing(ctx, listing, record)
}

// Deactivate retires the escrow before and after writing its row inactive, as an inactive registration does. See ../README.md, "Admin operations".
func (o *operations) Deactivate(ctx context.Context, id string) error {
	defer o.lockWrites(id)()
	if err := o.escrows.Retire(id); err != nil {
		return err
	}
	if err := o.store.SetDevshardActive(ctx, id, false); err != nil {
		return unknownDevshard(id, err)
	}
	return o.escrows.Retire(id)
}

func (o *operations) Settle(ctx context.Context, id string, force bool) (chain.SettleEscrowResult, error) {
	defer o.lockWrites(id)()
	if _, err := findDevshard(ctx, o.store, id); err != nil {
		return chain.SettleEscrowResult{}, unknownDevshard(id, err)
	}
	if err := o.escrows.Retire(id); err != nil {
		return chain.SettleEscrowResult{}, err
	}
	result, err := o.manager.Settle(ctx, id, force)
	return result, unknownDevshard(id, err)
}

func (o *operations) Unquarantine(_ context.Context, participantKey string) error {
	if !o.participants.ClearQuarantine(participantKey) {
		return fmt.Errorf("%w: %s", api.ErrUnknownParticipant, participantKey)
	}
	return nil
}

func (o *operations) ResetAccountingEpoch(_ context.Context, epoch uint64) (int, error) {
	return o.nonces.ResetEpoch(epoch)
}

func (o *operations) Reconfigure(ctx context.Context, overrides config.Overrides) error {
	next, err := config.Build(o.values, overrides)
	if err != nil {
		return err
	}
	if err := o.store.SaveOverrides(ctx, overrides); err != nil {
		return err
	}
	o.config.Swap(next)
	return nil
}
func escrowID(created uint64) string { return strconv.FormatUint(created, 10) }

func isChainEscrowID(id string) bool {
	parsed, err := strconv.ParseUint(id, 10, 64)
	return err == nil && escrowID(parsed) == id
}
