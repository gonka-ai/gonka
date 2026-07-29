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
	StatePath       string
	AppliedPath     string
	ReceiptsPath    string
	AuditPath       string
	AuditOutboxPath string
	LockPath        string
	JournalPath     string
	TemplatePath    string
	OutputPath      string
	NginxBinary     string
	ProxyPolicy     ProxyPolicy
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
	OperationID     string    `json:"operation_id"`
	Action          string    `json:"action,omitempty"`
	Host            string    `json:"host,omitempty"`
	MembershipID    string    `json:"membership_id,omitempty"`
	From            HostState `json:"from,omitempty"`
	To              HostState `json:"to,omitempty"`
	Target          HostState `json:"target,omitempty"`
	Phase           string    `json:"phase"`
	NewGeneration   uint64    `json:"new_generation"`
	RenderRevision  uint64    `json:"render_revision,omitempty"`
	RenderSourceSHA string    `json:"render_source_sha256,omitempty"`
	NewConfigSHA    string    `json:"new_config_sha256"`
	CreatedAt       time.Time `json:"created_at"`
}

type Status struct {
	State
	PendingOperation *PendingOperation `json:"pending_operation,omitempty"`
	Application      ApplicationStatus `json:"application"`
}

type AuditRecord struct {
	EventID      string    `json:"event_id,omitempty"`
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
	operationJournalSchemaVersion  = 1
	recoveryPolicyRollForward      = "roll_forward"
	operationPhaseIntentCommitted  = "intent_committed"
	operationPhaseDesiredPersisted = "desired_persisted"
	operationPhaseApplied          = "applied"
	operationPhaseAuditEnqueued    = "audit_enqueued"
)

type operationJournal struct {
	SchemaVersion   int               `json:"schema_version"`
	OperationID     string            `json:"operation_id"`
	Phase           string            `json:"phase"`
	Action          string            `json:"action,omitempty"`
	Host            string            `json:"host,omitempty"`
	MembershipID    string            `json:"membership_id,omitempty"`
	From            HostState         `json:"from,omitempty"`
	To              HostState         `json:"to,omitempty"`
	Target          HostState         `json:"target,omitempty"`
	Result          string            `json:"result,omitempty"`
	NewState        State             `json:"new_state"`
	Receipt         *operationReceipt `json:"completion_receipt,omitempty"`
	NewConfig       []byte            `json:"new_config,omitempty"`
	NewConfigSHA    string            `json:"new_config_sha256"`
	RenderRevision  uint64            `json:"render_revision,omitempty"`
	RenderSourceSHA string            `json:"render_source_sha256,omitempty"`
	Audit           AuditRecord       `json:"audit"`
	RecoveryPolicy  string            `json:"recovery_policy"`
	Reload          bool              `json:"reload"`
	CreatedAt       time.Time         `json:"created_at"`
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
	if config.AppliedPath == "" {
		config.AppliedPath = config.StatePath + ".applied.json"
	}
	if config.ReceiptsPath == "" {
		config.ReceiptsPath = config.StatePath + ".receipts.json"
	}
	if config.AuditOutboxPath == "" {
		config.AuditOutboxPath = config.StatePath + ".audit-outbox.json"
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
		if err := c.repairTornAuditTail(); err != nil {
			return err
		}
		if err := c.recoverPending(ctx, false); err != nil {
			return err
		}
		defer c.flushAuditBestEffort()
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
		if err := c.commitDesired(ctx, initial, mutation{
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
			if err := matchCompletedTransition(
				*completed,
				change,
			); err != nil {
				return err
			}
			result = current
			return nil
		}
		next := current.Clone()
		outcome, err := next.Advance(change)
		if err != nil {
			c.recordAuditBestEffort(AuditRecord{
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
			c.recordAuditBestEffort(AuditRecord{
				Time: time.Now().UTC(), OperationID: change.OperationID,
				Action: record.Action, Host: record.Host,
				MembershipID: record.MembershipID,
				From:         record.From, To: record.To, Target: record.Target,
				Generation: current.Generation, Result: "noop",
			})
			return nil
		}
		if err := c.commitDesired(ctx, next, record, true); err != nil {
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
			c.recordAuditBestEffort(AuditRecord{
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
			c.recordAuditBestEffort(AuditRecord{
				Time: time.Now().UTC(), OperationID: record.OperationID,
				Action: record.Action, Host: record.Host,
				MembershipID: record.MembershipID,
				From:         record.From, To: record.To, Target: record.Target,
				Generation: current.Generation, Result: "noop",
			})
			return nil
		}
		if err := c.commitDesired(ctx, next, record, true); err != nil {
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
			c.recordAuditBestEffort(AuditRecord{
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
			c.recordAuditBestEffort(AuditRecord{
				Time: time.Now().UTC(), OperationID: record.OperationID,
				Action: record.Action, Host: record.Host,
				MembershipID: record.MembershipID,
				From:         record.From, To: record.To, Target: record.Target,
				Generation: current.Generation, Result: "noop",
			})
			return nil
		}
		if err := c.commitDesired(ctx, next, record, true); err != nil {
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
		journal, err := c.loadOperationJournal()
		if err != nil {
			return err
		}
		desired := state
		var application ApplicationStatus
		if journal != nil &&
			journal.RecoveryPolicy == recoveryPolicyRollForward {
			desired = journal.NewState
			application, err = c.applicationStatusForConfig(
				desired,
				journal.NewConfig,
			)
		} else {
			application, err = c.applicationStatus(desired)
		}
		if err != nil {
			return err
		}
		status.State = desired
		status.Application = application
		if journal != nil {
			status.PendingOperation = &PendingOperation{
				OperationID:     journal.OperationID,
				Action:          journal.Action,
				Host:            journal.Host,
				MembershipID:    journal.MembershipID,
				From:            journal.From,
				To:              journal.To,
				Target:          journal.Target,
				Phase:           journal.Phase,
				NewGeneration:   journal.NewState.Generation,
				RenderRevision:  journal.RenderRevision,
				RenderSourceSHA: journal.RenderSourceSHA,
				NewConfigSHA:    journal.NewConfigSHA,
				CreatedAt:       journal.CreatedAt,
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
		defer c.flushAuditBestEffort()
		loaded, err := c.loadState()
		if err != nil {
			return err
		}
		if err := c.ensureRendered(ctx, loaded, true); err != nil {
			return err
		}
		state = loaded
		return nil
	})
	return state, err
}

// commitDesired makes the WAL the commit point, then asks the reconciler to
// converge all durable projections. Failures after the WAL never roll back.
func (c *Controller) commitDesired(
	ctx context.Context,
	newState State,
	change mutation,
	reload bool,
) error {
	receipts, err := c.loadOrCreateReceiptIndex()
	if err != nil {
		return err
	}
	var receipt *operationReceipt
	if deduplicatedMutation(change) && terminalResult(change.Result) {
		completed := receiptFromMutation(change)
		if err := receipts.Record(change.OperationID, completed); err != nil {
			return err
		}
		receipt = &completed
	}
	projection, err := c.renderProjection(newState)
	if err != nil {
		return err
	}
	journal := operationJournal{
		SchemaVersion:   operationJournalSchemaVersion,
		OperationID:     change.OperationID,
		Phase:           operationPhaseIntentCommitted,
		Action:          change.Action,
		Host:            change.Host,
		MembershipID:    change.MembershipID,
		From:            change.From,
		To:              change.To,
		Target:          change.Target,
		Result:          change.Result,
		NewState:        newState,
		Receipt:         receipt,
		NewConfig:       projection.Config,
		NewConfigSHA:    projection.ConfigSHA,
		RenderRevision:  1,
		RenderSourceSHA: projection.SourceSHA,
		Audit:           auditRecord(change, newState.Generation, projection.ConfigSHA),
		RecoveryPolicy:  recoveryPolicyRollForward,
		Reload:          reload,
		CreatedAt:       time.Now().UTC(),
	}
	if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
		return err
	}
	return c.reconcileCommitted(ctx, journal, reload)
}

func (c *Controller) ensureRendered(ctx context.Context, state State, reload bool) error {
	projection, err := c.renderProjection(state)
	if err != nil {
		return err
	}
	want := projection.Config
	applied, err := c.loadAppliedState()
	if err != nil {
		return err
	}
	got, outputErr := os.ReadFile(c.config.OutputPath)
	if outputErr == nil &&
		hashBytes(got) == hashBytes(want) &&
		applied != nil &&
		applied.Generation == state.Generation &&
		applied.ConfigSHA == hashBytes(want) {
		return c.runner.Run(ctx, c.config.NginxBinary, "-t")
	}
	if outputErr != nil && !errors.Is(outputErr, fs.ErrNotExist) {
		return outputErr
	}
	return c.commitDesired(ctx, state, mutation{
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
		c.flushAuditBestEffort()
		return nil
	}
	if journal.RecoveryPolicy != recoveryPolicyRollForward {
		return fmt.Errorf(
			"recover operation %s: unknown recovery policy %q",
			journal.OperationID,
			journal.RecoveryPolicy,
		)
	}
	return c.reconcileCommitted(ctx, *journal, reload)
}

func (c *Controller) loadOperationJournal() (*operationJournal, error) {
	data, err := os.ReadFile(c.config.JournalPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journal operationJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("decode operation journal: %w", err)
	}
	if journal.SchemaVersion != operationJournalSchemaVersion {
		return nil, fmt.Errorf(
			"unsupported operation journal schema %d",
			journal.SchemaVersion,
		)
	}
	return &journal, nil
}

// reconcileCommitted materializes a committed intent in dependency order:
// desired state, nginx application, audit outbox.
func (c *Controller) reconcileCommitted(
	ctx context.Context,
	journal operationJournal,
	reload bool,
) error {
	if err := journal.NewState.Validate(); err != nil {
		return fmt.Errorf("recover operation %s: invalid new state: %w", journal.OperationID, err)
	}
	if journal.Receipt != nil {
		if err := validateOperationReceipt(
			journal.OperationID,
			*journal.Receipt,
		); err != nil {
			return fmt.Errorf(
				"recover operation %s: invalid completion receipt: %w",
				journal.OperationID,
				err,
			)
		}
	}
	if err := validateCommittedProjection(journal); err != nil {
		return fmt.Errorf("recover operation %s: %w", journal.OperationID, err)
	}
	if err := c.refreshCommittedProjection(&journal); err != nil {
		return fmt.Errorf(
			"refresh operation %s config projection: %w",
			journal.OperationID,
			err,
		)
	}
	if err := validateCommittedProjection(journal); err != nil {
		return fmt.Errorf("recover operation %s: %w", journal.OperationID, err)
	}
	if err := writeJSONAtomic(c.config.StatePath, journal.NewState, 0o600); err != nil {
		return err
	}
	if journal.Receipt != nil {
		if err := c.persistCompletionReceipt(
			journal.OperationID,
			*journal.Receipt,
		); err != nil {
			return err
		}
	}
	journal.Phase = operationPhaseDesiredPersisted
	if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
		return err
	}
	if err := c.reconcile(
		ctx,
		journal.NewState,
		journal.NewConfig,
		journal.OperationID,
		journal.Reload && reload,
	); err != nil {
		return fmt.Errorf(
			"reconcile operation %s: %w",
			journal.OperationID,
			err,
		)
	}
	journal.Phase = operationPhaseApplied
	if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
		return err
	}
	if err := c.enqueueAudit(journal.Audit); err != nil {
		c.recordAuditBestEffort(journal.Audit)
	} else {
		journal.Phase = operationPhaseAuditEnqueued
		if err := writeJSONAtomic(c.config.JournalPath, journal, 0o600); err != nil {
			return err
		}
	}
	if err := os.Remove(c.config.JournalPath); err != nil {
		return err
	}
	c.flushAuditBestEffort()
	return nil
}

func (c *Controller) loadState() (State, error) {
	data, err := os.ReadFile(c.config.StatePath)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode router state: %w", err)
	}
	if err := state.Validate(); err != nil {
		return State{}, err
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
	if err := c.repairTornAuditTail(); err != nil {
		return err
	}
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
	actionMatches := receipt.Action == action
	membershipMatches := membershipID == "" ||
		receipt.MembershipID == "" ||
		receipt.MembershipID == membershipID
	if actionMatches &&
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

func matchCompletedTransition(
	receipt operationReceipt,
	change Transition,
) error {
	action := "transfer"
	if receipt.Action == "add" &&
		change.From == HostJoining &&
		change.To == HostActive &&
		change.Target == HostActive {
		action = "add"
	}
	return matchCompletedOperation(
		receipt,
		change.OperationID,
		action,
		change.Host,
		change.MembershipID,
		change.Target,
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

func auditRecord(
	change mutation,
	generation uint64,
	configSHA string,
) AuditRecord {
	record := AuditRecord{
		Time:         time.Now().UTC(),
		OperationID:  change.OperationID,
		Action:       change.Action,
		Host:         change.Host,
		MembershipID: change.MembershipID,
		From:         change.From,
		To:           change.To,
		Target:       change.Target,
		Generation:   generation,
		Result:       change.Result,
	}
	record.EventID = auditEventID(record, configSHA)
	return record
}

func auditEventID(record AuditRecord, configSHA string) string {
	return "router-audit-" + hashBytes([]byte(fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s",
		record.OperationID,
		record.Action,
		record.Host,
		record.MembershipID,
		record.From,
		record.To,
		record.Target,
		record.Generation,
		record.Result,
		configSHA,
	)))
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
