package process

type Conditions struct {
	Available   bool
	Progressing bool
	Reconciled  bool
	Degraded    bool
	// Serving means at least one running child whose live readiness is current.
	// Available says a child process exists; Serving says it can take a request
	// right now. A child that is draining or whose storage became unavailable is
	// running but not serving.
	Serving bool
	// Converged latches once the manager has run every desired version at least
	// once. It never clears, so a later download or restart does not retract it.
	Converged      bool
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
	conditions.Serving = servingChildrenLocked(m.processes) > 0
	converged := conditions.Running == conditions.Desired
	if converged && conditions.Desired > 0 {
		m.everConverged = true
	}
	progressing := !converged || len(m.downloading) > 0
	conditions.Reconciled = conditions.Reconciled && !progressing
	conditions.Progressing = progressing && conditions.ReconcileError == ""
	conditions.Degraded = conditions.ReconcileError != ""
	conditions.Converged = m.everConverged
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

// servingChildrenLocked counts running children the readiness monitor currently
// vouches for — the same predicate ServesVersion applies per version, so the
// coarse and the per-version answers cannot drift apart.
func servingChildrenLocked(children map[string]*child) int {
	serving := 0
	for _, child := range children {
		if child.status == statusRunning && child.servingFresh() {
			serving++
		}
	}
	return serving
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
