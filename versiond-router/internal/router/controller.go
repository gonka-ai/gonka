package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type Config struct {
	StatePath    string
	ReceiptsPath string
	AuditPath    string
	LockPath     string
	JournalPath  string
	TemplatePath string
	OutputPath   string
	NginxBinary  string
	ProxyPolicy  ProxyPolicy
}

type CommandRunner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, output)
	}
	return nil
}

type Controller struct {
	config Config
	runner CommandRunner
}

// InitialStateFactory supplies bootstrap input only when no authoritative
// state exists.
type InitialStateFactory func() (State, error)

type PendingOperation struct {
	OperationID   string    `json:"operation_id"`
	Action        string    `json:"action,omitempty"`
	Host          string    `json:"host,omitempty"`
	MembershipID  string    `json:"membership_id,omitempty"`
	From          HostState `json:"from,omitempty"`
	To            HostState `json:"to,omitempty"`
	Target        HostState `json:"target,omitempty"`
	Phase         string    `json:"phase"`
	NewGeneration uint64    `json:"new_generation"`
	NewConfigSHA  string    `json:"new_config_sha256"`
	CreatedAt     time.Time `json:"created_at"`
}

type Status struct {
	State
	PendingOperation *PendingOperation `json:"pending_operation,omitempty"`
}

type AuditRecord struct {
	Time         time.Time `json:"time"`
	OperationID  string    `json:"operation_id"`
	Action       string    `json:"action"`
	Host         string    `json:"host,omitempty"`
	MembershipID string    `json:"membership_id,omitempty"`
	From         HostState `json:"from,omitempty"`
	To           HostState `json:"to,omitempty"`
	Target       HostState `json:"target,omitempty"`
	Generation   uint64    `json:"generation"`
	Result       string    `json:"result"`
	Error        string    `json:"error,omitempty"`
}

const (
	legacyOperationJournalSchemaVersion = 1
	operationJournalSchemaVersion       = 2
)

type operationJournal struct {
	SchemaVersion int           `json:"schema_version"`
	OperationID   string        `json:"operation_id"`
	Phase         string        `json:"phase"`
	Action        string        `json:"action,omitempty"`
	Host          string        `json:"host,omitempty"`
	MembershipID  string        `json:"membership_id,omitempty"`
	From          HostState     `json:"from,omitempty"`
	To            HostState     `json:"to,omitempty"`
	Target        HostState     `json:"target,omitempty"`
	Result        string        `json:"result,omitempty"`
	OldState      *State        `json:"old_state,omitempty"`
	NewState      State         `json:"new_state"`
	OldReceipts   *receiptIndex `json:"old_receipts,omitempty"`
	NewReceipts   receiptIndex  `json:"new_receipts"`
	OldConfig     []byte        `json:"old_config,omitempty"`
	NewConfigSHA  string        `json:"new_config_sha256"`
	Reload        bool          `json:"reload"`
	CreatedAt     time.Time     `json:"created_at"`
}

type mutation struct {
	OperationID   string
	Action        string
	ReceiptAction string
	Host          string
	MembershipID  string
	From          HostState
	To            HostState
	Target        HostState
	Result        string
}

func NewController(config Config, runner CommandRunner) *Controller {
	if config.JournalPath == "" {
		config.JournalPath = config.StatePath + ".operation.json"
	}
	if config.ReceiptsPath == "" {
		config.ReceiptsPath = config.StatePath + ".receipts.json"
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	config.ProxyPolicy = config.ProxyPolicy.withDefaults()
	return &Controller{config: config, runner: runner}
}

func (c *Controller) Bootstrap(
	ctx context.Context,
	newInitialState InitialStateFactory,
) (State, error) {
	var result State
	err := c.withLock(ctx, func() error {
		if err := c.recoverPending(ctx, false); err != nil {
			return err
		}
		current, err := c.loadState()
		if err == nil {
			if _, err := c.loadOrCreateReceiptIndex(); err != nil {
				return err
			}
			result = current
			return c.ensureRendered(ctx, current, false)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if newInitialState == nil {
			return errors.New("initial state factory is required when router state is absent")
		}
		initial, err := newInitialState()
		if err != nil {
			return err
		}
		if err := initial.Validate(); err != nil {
			return err
		}
		if err := c.apply(ctx, nil, initial, mutation{
			OperationID: "bootstrap",
			Action:      "bootstrap",
			Result:      "completed",
		}, false); err != nil {
			return err
		}
		result = initial
		return nil
	})
	return result, err
}

func (c *Controller) Transition(ctx context.Context, change Transition) (State, error) {
	var result State
	err := c.withLock(ctx, func() error {
		if err := c.recoverPending(ctx, true); err != nil {
			return err
		}
		current, err := c.loadState()
		if err != nil {
			return err
		}
		completed, err := c.completedOperation(change.OperationID)
		if err != nil {
			return err
		}
		if completed != nil {
			if err := matchCompletedOperation(
				*completed,
				change.OperationID,
				"transfer",
				change.Host,
				change.MembershipID,
				change.Target,
			); err != nil {
				return err
			}
			result = current
			return nil
		}
		next := current.Clone()
		outcome, err := next.Advance(change)
		if err != nil {
			_ = c.appendAudit(AuditRecord{
				Time: time.Now().UTC(), OperationID: change.OperationID,
				Action: "transfer", Host: change.Host,
				MembershipID: change.MembershipID,
				From:         change.From, To: change.To, Target: change.Target,
				Generation: current.Generation,
				Result:     "rejected", Error: err.Error(),
			})
			return err
		}
		record := mutation{
			OperationID:  change.OperationID,
			Action:       "transfer",
			Host:         change.Host,
			MembershipID: outcome.MembershipID,
			From:         outcome.From,
			To:           outcome.To,
			Target:       outcome.Target,
			Result:       transitionResult(outcome),
		}
		if outcome.Completed &&
			current.ActiveTransfer != nil &&
			current.ActiveTransfer.From == HostJoining &&
			current.ActiveTransfer.To == HostActive {
			record.ReceiptAction = "add"
		}
		if !outcome.Changed {
			result = current
			return c.appendAudit(AuditRecord{
				Time: time.Now().UTC(), OperationID: change.OperationID,
				Action: record.Action, Host: record.Host,
				MembershipID: record.MembershipID,
				From:         record.From, To: record.To, Target: record.Target,
				Generation: current.Generation, Result: "noop",
			})
		}
		if err := c.apply(ctx, &current, next, record, true); err != nil {
			return err
		}
		result = next
		return nil
	})
	return result, err
}

func (c *Controller) Add(ctx context.Context, change AddMembership) (State, error) {
	var result State
	err := c.withLock(ctx, func() error {
		if err := c.recoverPending(ctx, true); err != nil {
			return err
		}
		current, err := c.loadState()
		if err != nil {
			return err
		}
		completed, err := c.completedOperation(change.OperationID)
		if err != nil {
			return err
		}
		if completed != nil {
			if err := matchCompletedOperation(
				*completed,
				change.OperationID,
				"add",
				change.Host,
				change.MembershipID,
				HostActive,
			); err != nil {
				return err
			}
			result = current
			return nil
		}
		next := current.Clone()
		outcome, err := next.Add(change)
		if err != nil {
			_ = c.appendAudit(AuditRecord{
				Time: time.Now().UTC(), OperationID: change.OperationID,
				Action: "add", Host: change.Host,
				MembershipID: change.MembershipID,
				To:           HostJoining, Target: HostActive,
				Generation: current.Generation,
				Result:     "rejected", Error: err.Error(),
			})
			return err
		}
		record := mutation{
			OperationID:  change.OperationID,
			Action:       "add",
			Host:         change.Host,
			MembershipID: outcome.MembershipID,
			From:         outcome.From,
			To:           outcome.To,
			Target:       outcome.Target,
			Result:       transitionResult(outcome),
		}
		if !outcome.Changed {
			result = current
			return c.appendAudit(AuditRecord{
				Time: time.Now().UTC(), OperationID: record.OperationID,
				Action: record.Action, Host: record.Host,
				MembershipID: record.MembershipID,
				From:         record.From, To: record.To, Target: record.Target,
				Generation: current.Generation, Result: "noop",
			})
		}
		if err := c.apply(ctx, &current, next, record, true); err != nil {
			return err
		}
		result = next
		return nil
	})
	return result, err
}

func (c *Controller) Cancel(ctx context.Context, change CancelTransfer) (State, error) {
	var result State
	err := c.withLock(ctx, func() error {
		if err := c.recoverPending(ctx, true); err != nil {
			return err
		}
		current, err := c.loadState()
		if err != nil {
			return err
		}
		completed, err := c.completedOperation(change.OperationID)
		if err != nil {
			return err
		}
		if completed != nil {
			if err := matchCompletedOperation(
				*completed,
				change.OperationID,
				"cancel",
				change.Host,
				change.MembershipID,
				HostActive,
			); err != nil {
				return err
			}
			result = current
			return nil
		}
		next := current.Clone()
		outcome, err := next.Cancel(change)
		if err != nil {
			_ = c.appendAudit(AuditRecord{
				Time: time.Now().UTC(), OperationID: change.OperationID,
				Action: "cancel", Host: change.Host,
				MembershipID: change.MembershipID,
				From:         HostDraining, To: HostActive, Target: HostActive,
				Generation: current.Generation,
				Result:     "rejected", Error: err.Error(),
			})
			return err
		}
		record := mutation{
			OperationID:  change.OperationID,
			Action:       "cancel",
			Host:         change.Host,
			MembershipID: outcome.MembershipID,
			From:         outcome.From,
			To:           outcome.To,
			Target:       outcome.Target,
			Result:       transitionResult(outcome),
		}
		if !outcome.Changed {
			result = current
			return c.appendAudit(AuditRecord{
				Time: time.Now().UTC(), OperationID: record.OperationID,
				Action: record.Action, Host: record.Host,
				MembershipID: record.MembershipID,
				From:         record.From, To: record.To, Target: record.Target,
				Generation: current.Generation, Result: "noop",
			})
		}
		if err := c.apply(ctx, &current, next, record, true); err != nil {
			return err
		}
		result = next
		return nil
	})
	return result, err
}

func (c *Controller) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.withLock(ctx, func() error {
		state, err := c.loadState()
		if err != nil {
			return err
		}
		status.State = state
		journal, err := c.loadOperationJournal()
		if err != nil {
			return err
		}
		if journal != nil {
			status.PendingOperation = &PendingOperation{
				OperationID:   journal.OperationID,
				Action:        journal.Action,
				Host:          journal.Host,
				MembershipID:  journal.MembershipID,
				From:          journal.From,
				To:            journal.To,
				Target:        journal.Target,
				Phase:         journal.Phase,
				NewGeneration: journal.NewState.Generation,
				NewConfigSHA:  journal.NewConfigSHA,
				CreatedAt:     journal.CreatedAt,
			}
		}
		return nil
	})
	return status, err
}

func (c *Controller) Recover(ctx context.Context) (State, error) {
	var state State
	err := c.withLock(ctx, func() error {
		if err := c.recoverPending(ctx, true); err != nil {
			return err
		}
		loaded, err := c.loadState()
		state = loaded
		return err
	})
	return state, err
}

func (c *Controller) apply(
	ctx context.Context,
	oldState *State,
	newState State,
	change mutation,
	reload bool,
) error {
	oldReceipts, err := c.loadOrCreateReceiptIndex()
	if err != nil {
		return err
	}
	newReceipts := oldReceipts.Clone()
	if deduplicatedMutation(change) && terminalResult(change.Result) {
		if err := newReceipts.Record(
			change.OperationID,
			receiptFromMutation(change),
		); err != nil {
			return err
		}
	}
	if err := newReceipts.Validate(); err != nil {
		return err
	}
	template, err := os.ReadFile(c.config.TemplatePath)
	if err != nil {
		return err
	}
	newConfig, err := Render(template, newState, c.config.ProxyPolicy)
	if err != nil {
		return err
	}
	oldConfig, readErr := os.ReadFile(c.config.OutputPath)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	journal := operationJournal{
		SchemaVersion: operationJournalSchemaVersion,
		OperationID:   change.OperationID,
		Phase:         "prepared",
		Action:        change.Action,
		Host:          change.Host,
		MembershipID:  change.MembershipID,
		From:          change.From,
		To:            change.To,
		Target:        change.Target,
		Result:        change.Result,
		OldState:      oldState,
		NewState:      newState,
		OldReceipts:   &oldReceipts,
		NewReceipts:   newReceipts,
		OldConfig:     oldConfig,
		NewConfigSHA:  hashBytes(newConfig),
		Reload:        reload,
		CreatedAt:     time.Now().UTC(),
	}
	if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
		return err
	}

	rollback := func(applyErr error) error {
		rollbackErr := c.restoreConfig(ctx, oldConfig, reload)
		if oldState == nil {
			if err := os.Remove(c.config.StatePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		} else if err := writeJSONAtomic(c.config.StatePath, *oldState, 0o600); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
		if err := writeJSONAtomic(
			c.config.ReceiptsPath,
			oldReceipts,
			0o600,
		); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
		record := AuditRecord{
			Time: time.Now().UTC(), OperationID: change.OperationID, Action: change.Action,
			Host: change.Host, MembershipID: change.MembershipID,
			From: change.From, To: change.To, Target: change.Target,
			Generation: newState.Generation,
			Result:     "failed", Error: applyErr.Error(),
		}
		_ = c.appendAudit(record)
		if rollbackErr == nil {
			_ = os.Remove(c.config.JournalPath)
			return applyErr
		}
		return errors.Join(applyErr, fmt.Errorf("rollback: %w", rollbackErr))
	}

	if err := writeFileAtomic(c.config.OutputPath, newConfig, 0o644); err != nil {
		return rollback(err)
	}
	journal.Phase = "config_published"
	if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
		return rollback(err)
	}
	if err := c.runner.Run(ctx, c.config.NginxBinary, "-t"); err != nil {
		return rollback(fmt.Errorf("validate nginx config: %w", err))
	}
	if reload {
		if err := c.runner.Run(ctx, c.config.NginxBinary, "-s", "reload"); err != nil {
			return rollback(fmt.Errorf("reload nginx: %w", err))
		}
		journal.Phase = "reloaded"
		if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
			return rollback(err)
		}
	}
	if err := writeJSONAtomic(c.config.StatePath, newState, 0o600); err != nil {
		return rollback(err)
	}
	if err := writeJSONAtomic(c.config.ReceiptsPath, newReceipts, 0o600); err != nil {
		return rollback(err)
	}
	if err := c.appendAudit(AuditRecord{
		Time: time.Now().UTC(), OperationID: change.OperationID, Action: change.Action,
		Host: change.Host, MembershipID: change.MembershipID,
		From: change.From, To: change.To, Target: change.Target,
		Generation: newState.Generation, Result: change.Result,
	}); err != nil {
		return rollback(err)
	}
	return os.Remove(c.config.JournalPath)
}

func (c *Controller) ensureRendered(ctx context.Context, state State, reload bool) error {
	template, err := os.ReadFile(c.config.TemplatePath)
	if err != nil {
		return err
	}
	want, err := Render(template, state, c.config.ProxyPolicy)
	if err != nil {
		return err
	}
	got, err := os.ReadFile(c.config.OutputPath)
	if err == nil && hashBytes(got) == hashBytes(want) {
		return c.runner.Run(ctx, c.config.NginxBinary, "-t")
	}
	return c.apply(ctx, &state, state, mutation{
		OperationID: "bootstrap-render",
		Action:      "bootstrap-render",
		Result:      "completed",
	}, reload)
}

func (c *Controller) recoverPending(ctx context.Context, reload bool) error {
	journal, err := c.loadOperationJournal()
	if err != nil {
		return err
	}
	if journal == nil {
		return nil
	}
	switch journal.Phase {
	case "prepared", "config_published":
		return c.rollBackPending(ctx, *journal, reload)
	case "reloaded":
		return c.rollForwardPending(ctx, *journal, reload)
	default:
		return fmt.Errorf(
			"recover operation %s: unknown journal phase %q",
			journal.OperationID,
			journal.Phase,
		)
	}
}

func (c *Controller) loadOperationJournal() (*operationJournal, error) {
	data, err := os.ReadFile(c.config.JournalPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	journal, migrated, err := c.decodeOperationJournal(data)
	if err != nil {
		return nil, err
	}
	if migrated {
		if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
			return nil, fmt.Errorf(
				"persist migrated operation journal: %w",
				err,
			)
		}
	}
	return &journal, nil
}

func (c *Controller) rollBackPending(
	ctx context.Context,
	journal operationJournal,
	reload bool,
) error {
	if err := c.restoreConfig(ctx, journal.OldConfig, journal.Reload && reload); err != nil {
		return fmt.Errorf("recover operation %s: %w", journal.OperationID, err)
	}
	if journal.OldState == nil {
		if err := os.Remove(c.config.StatePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else if err := writeJSONAtomic(c.config.StatePath, *journal.OldState, 0o600); err != nil {
		return err
	}
	if journal.OldReceipts == nil {
		if err := os.Remove(c.config.ReceiptsPath); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else if err := writeJSONAtomic(
		c.config.ReceiptsPath,
		*journal.OldReceipts,
		0o600,
	); err != nil {
		return err
	}
	if err := c.appendRecoveryAudit(journal, "rolled_back"); err != nil {
		return err
	}
	return os.Remove(c.config.JournalPath)
}

func (c *Controller) rollForwardPending(
	ctx context.Context,
	journal operationJournal,
	reload bool,
) error {
	if err := journal.NewState.Validate(); err != nil {
		return fmt.Errorf("recover operation %s: invalid new state: %w", journal.OperationID, err)
	}
	if err := journal.NewReceipts.Validate(); err != nil {
		return fmt.Errorf(
			"recover operation %s: invalid receipt index: %w",
			journal.OperationID,
			err,
		)
	}
	config, err := os.ReadFile(c.config.OutputPath)
	if err != nil {
		return fmt.Errorf("recover operation %s: read published config: %w", journal.OperationID, err)
	}
	if got := hashBytes(config); got != journal.NewConfigSHA {
		return fmt.Errorf(
			"recover operation %s: published config sha256 is %s, want %s",
			journal.OperationID,
			got,
			journal.NewConfigSHA,
		)
	}
	if err := c.runner.Run(ctx, c.config.NginxBinary, "-t"); err != nil {
		return fmt.Errorf("recover operation %s: validate nginx config: %w", journal.OperationID, err)
	}
	if journal.Reload && reload {
		if err := c.runner.Run(ctx, c.config.NginxBinary, "-s", "reload"); err != nil {
			return fmt.Errorf("recover operation %s: reload nginx: %w", journal.OperationID, err)
		}
	}
	if err := writeJSONAtomic(c.config.StatePath, journal.NewState, 0o600); err != nil {
		return err
	}
	if err := writeJSONAtomic(
		c.config.ReceiptsPath,
		journal.NewReceipts,
		0o600,
	); err != nil {
		return err
	}
	if err := c.appendRecoveryAudit(journal, "rolled_forward"); err != nil {
		return err
	}
	return os.Remove(c.config.JournalPath)
}

func (c *Controller) appendRecoveryAudit(journal operationJournal, result string) error {
	to := journal.To
	if to == "" {
		if index := journal.NewState.hostIndex(journal.Host); index >= 0 {
			to = journal.NewState.Hosts[index].State
		}
	}
	action := journal.Action
	if action == "" {
		action = "recovery"
	}
	if result == "rolled_forward" && journal.Result != "" {
		result = journal.Result
	}
	if err := c.appendAudit(AuditRecord{
		Time: time.Now().UTC(), OperationID: journal.OperationID,
		Action: action, Host: journal.Host, From: journal.From, To: to,
		MembershipID: journal.MembershipID, Target: journal.Target,
		Generation: journal.NewState.Generation, Result: result,
		Error: "recovered incomplete operation at phase " + journal.Phase,
	}); err != nil {
		return err
	}
	return nil
}

func (c *Controller) restoreConfig(ctx context.Context, oldConfig []byte, reload bool) error {
	if len(oldConfig) == 0 {
		if err := os.Remove(c.config.OutputPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := writeFileAtomic(c.config.OutputPath, oldConfig, 0o644); err != nil {
		return err
	}
	if !reload {
		return nil
	}
	if err := c.runner.Run(ctx, c.config.NginxBinary, "-t"); err != nil {
		return err
	}
	return c.runner.Run(ctx, c.config.NginxBinary, "-s", "reload")
}

func (c *Controller) loadState() (State, error) {
	data, err := os.ReadFile(c.config.StatePath)
	if err != nil {
		return State{}, err
	}
	state, migrated, err := decodeState(data)
	if err != nil {
		return State{}, err
	}
	if migrated {
		if err := writeJSONAtomic(c.config.StatePath, state, 0o600); err != nil {
			return State{}, fmt.Errorf("persist migrated router state: %w", err)
		}
	}
	return state, nil
}

func (c *Controller) withLock(ctx context.Context, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(c.config.LockPath), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(c.config.LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func (c *Controller) appendAudit(record AuditRecord) error {
	if err := os.MkdirAll(filepath.Dir(c.config.AuditPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(c.config.AuditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(f)
	if err := encoder.Encode(record); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (c *Controller) completedOperation(operationID string) (*operationReceipt, error) {
	if operationID == "" {
		return nil, nil
	}
	index, err := c.loadOrCreateReceiptIndex()
	if err != nil {
		return nil, err
	}
	receipt, ok := index.Completed[operationID]
	if !ok {
		return nil, nil
	}
	return &receipt, nil
}

func matchCompletedOperation(
	receipt operationReceipt,
	operationID string,
	action string,
	host string,
	membershipID string,
	target HostState,
) error {
	actionMatches := receipt.ActionAgnostic || receipt.Action == action
	membershipMatches := membershipID == "" ||
		receipt.MembershipID == "" ||
		receipt.MembershipID == membershipID
	if !receipt.Conflict &&
		actionMatches &&
		receipt.Host == host &&
		membershipMatches &&
		receipt.Target == target {
		return nil
	}
	return fmt.Errorf(
		"%w: operation %s already completed as %s for membership %s (%s)",
		ErrOperationOwner,
		operationID,
		receipt.Action,
		receipt.MembershipID,
		receipt.Host,
	)
}

func transitionResult(result TransitionResult) string {
	switch {
	case result.Canceled:
		return "canceled"
	case result.Completed:
		return "completed"
	default:
		return "advanced"
	}
}

func terminalResult(result string) bool {
	return result == "completed" || result == "canceled"
}

func deduplicatedMutation(change mutation) bool {
	switch change.Action {
	case "transfer", "add", "cancel":
		return true
	default:
		return false
	}
}

func writeJSONAtomic(path string, value any, mode fs.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(path, data, mode)
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".routerctl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	return dirFile.Sync()
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
