package gatewayphase

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Epoch phases and confirmation PoC phases as the gateway's phase gate reads them.
const (
	PhaseInference           = "Inference"
	PhasePoCGenerate         = "PoCGenerate"
	PhasePoCGenerateWindDown = "PoCGenerateWindDown"
	PhasePoCValidate         = "PoCValidate"
	PhasePoCValidateWindDown = "PoCValidateWindDown"

	ConfirmationPoCGracePeriod = "CONFIRMATION_POC_GRACE_PERIOD"
	ConfirmationPoCGeneration  = "CONFIRMATION_POC_GENERATION"
	ConfirmationPoCValidation  = "CONFIRMATION_POC_VALIDATION"
)

var (
	epochPhases = map[string]struct{}{
		PhaseInference: {}, PhasePoCGenerate: {}, PhasePoCGenerateWindDown: {},
		PhasePoCValidate: {}, PhasePoCValidateWindDown: {},
	}
	confirmationPhases = map[string]struct{}{
		"": {}, ConfirmationPoCGracePeriod: {}, ConfirmationPoCGeneration: {}, ConfirmationPoCValidation: {},
	}
)

// Step is one entry of a phase timeline: from At after the schedule start
// until the next step, the stub reports Phase and ConfirmationPoCPhase.
type Step struct {
	At                   time.Duration
	Phase                string
	ConfirmationPoCPhase string
}

// ConfirmationPoCActive reports whether the step is inside a confirmation PoC.
func (s Step) ConfirmationPoCActive() bool {
	return s.ConfirmationPoCPhase != ""
}

func (s Step) validate() error {
	if _, ok := epochPhases[s.Phase]; !ok {
		return fmt.Errorf("unknown phase %q", s.Phase)
	}
	if _, ok := confirmationPhases[s.ConfirmationPoCPhase]; !ok {
		return fmt.Errorf("unknown confirmation_poc_phase %q", s.ConfirmationPoCPhase)
	}
	if s.At < 0 {
		return fmt.Errorf("step at must not be negative")
	}
	return nil
}

// Schedule is a phase timeline. Before the first step and with no steps it
// reports Inference; the last step holds until the schedule is replaced.
type Schedule struct {
	mu    sync.Mutex
	now   func() time.Time
	start time.Time
	steps []Step
}

// NewSchedule starts steps now. Steps must already be valid (see ParseSteps).
func NewSchedule(steps []Step) *Schedule {
	return NewScheduleWithClock(steps, time.Now)
}

// NewScheduleWithClock is NewSchedule with an injected clock (tests).
func NewScheduleWithClock(steps []Step, now func() time.Time) *Schedule {
	s := &Schedule{now: now}
	s.Set(steps)
	return s
}

// Set replaces the timeline and restarts it at the current time.
func (s *Schedule) Set(steps []Step) {
	sorted := append([]Step(nil), steps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At < sorted[j].At })
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start = s.now()
	s.steps = sorted
}

// Current returns the step in effect now.
func (s *Schedule) Current() Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLocked()
}

func (s *Schedule) currentLocked() Step {
	elapsed := s.now().Sub(s.start)
	current := Step{Phase: PhaseInference}
	for _, step := range s.steps {
		if step.At > elapsed {
			break
		}
		current = step
	}
	return current
}

// StepJSON is the wire form of a Step, with At as a Go duration string.
type StepJSON struct {
	At                   string `json:"at"`
	Phase                string `json:"phase"`
	ConfirmationPoCPhase string `json:"confirmation_poc_phase,omitempty"`
}

func (s Step) wire() StepJSON {
	return StepJSON{At: s.At.String(), Phase: s.Phase, ConfirmationPoCPhase: s.ConfirmationPoCPhase}
}

// ScheduleSnapshot is the GET/POST /testenv/phase response.
type ScheduleSnapshot struct {
	StartedAt time.Time  `json:"started_at"`
	Current   StepJSON   `json:"current"`
	Steps     []StepJSON `json:"steps"`
}

// Snapshot reports the timeline and the step in effect.
func (s *Schedule) Snapshot() ScheduleSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ScheduleSnapshot{StartedAt: s.start, Current: s.currentLocked().wire(), Steps: make([]StepJSON, 0, len(s.steps))}
	for _, step := range s.steps {
		out.Steps = append(out.Steps, step.wire())
	}
	return out
}

// ScheduleRequest is the POST /testenv/phase body. Steps start at POST time;
// an empty list returns to Inference.
type ScheduleRequest struct {
	Steps stepRequests `json:"steps"`
}

type stepRequests []StepJSON

func (r stepRequests) parse() ([]Step, error) {
	steps := make([]Step, 0, len(r))
	for i, raw := range r {
		at, err := time.ParseDuration(raw.At)
		if err != nil {
			return nil, fmt.Errorf("steps[%d].at: %w", i, err)
		}
		step := Step{At: at, Phase: raw.Phase, ConfirmationPoCPhase: raw.ConfirmationPoCPhase}
		if step.Phase == "" {
			step.Phase = PhaseInference
		}
		if err := step.validate(); err != nil {
			return nil, fmt.Errorf("steps[%d]: %w", i, err)
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// ParseSteps parses "at=phase[/confirmation_phase],..." such as
// "7m30s=PoCGenerate,8m10s=PoCValidate,8m30s=Inference" (MOCK_DAPI_PHASE_SCHEDULE).
func ParseSteps(raw string) ([]Step, error) {
	var r stepRequests
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		at, phases, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid step %q (want at=phase)", part)
		}
		phase, confirmation, _ := strings.Cut(phases, "/")
		r = append(r, StepJSON{At: strings.TrimSpace(at), Phase: strings.TrimSpace(phase), ConfirmationPoCPhase: strings.TrimSpace(confirmation)})
	}
	return r.parse()
}
