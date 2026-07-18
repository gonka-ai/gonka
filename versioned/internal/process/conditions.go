package process

import "fmt"

type Conditions struct {
	Available      bool
	Reconciled     bool
	Degraded       bool
	Desired        int
	Running        int
	ReconcileError string
}

func (m *Manager) Conditions() Conditions {
	m.mu.Lock()
	defer m.mu.Unlock()
	conditions := m.conditions
	conditions.Running = runningChildrenLocked(m.processes)
	conditions.Available = conditions.Running > 0
	converged := conditions.Running == conditions.Desired
	conditions.Reconciled = conditions.Reconciled && converged
	conditions.Degraded = conditions.Available && !conditions.Reconciled
	if !converged && conditions.ReconcileError == "" {
		conditions.ReconcileError = fmt.Sprintf(
			"running %d of %d desired child generations",
			conditions.Running,
			conditions.Desired,
		)
	}
	return conditions
}

func (m *Manager) recordReconcileResult(desired int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conditions.Desired = desired
	m.conditions.Reconciled = err == nil
	m.conditions.ReconcileError = ""
	if err != nil {
		m.conditions.ReconcileError = err.Error()
	}
}

// ReportReconcileError records a failure before Manager.Reconcile can run,
// such as an unavailable or invalid oracle response.
func (m *Manager) ReportReconcileError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.conditions.Reconciled = false
	m.conditions.ReconcileError = err.Error()
	m.mu.Unlock()
}

func runningChildrenLocked(children map[string]*child) int {
	running := 0
	for _, child := range children {
		if child.status == statusRunning {
			running++
		}
	}
	return running
}
