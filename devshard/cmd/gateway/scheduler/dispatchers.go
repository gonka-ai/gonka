package scheduler

// Stop shuts every escrow's actor down and is safe to call more than once.
func (s *Scheduler) Stop() {
	s.registryMu.Lock()
	s.stopped = true
	running := make([]*dispatcher, 0, len(s.dispatchers)+len(s.superseded))
	for _, active := range s.dispatchers {
		running = append(running, active)
	}
	for superseded := range s.superseded {
		running = append(running, superseded)
	}
	clear(s.dispatchers)
	clear(s.superseded)
	s.registryMu.Unlock()

	for _, active := range running {
		active.stop()
	}
}

func (s *Scheduler) dispatcherFor(escrow Escrow) (*dispatcher, error) {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	if s.stopped {
		return nil, ErrDispatcherStopped
	}

	target, known := s.dispatchers[escrow.ID]
	live := known && !target.isStopped()
	if live && target.sessionID != escrow.SessionID {
		s.supersede(target)
	}
	if !live || target.sessionID != escrow.SessionID {
		target = newDispatcher(dispatcherDeps{
			escrowID:            escrow.ID,
			sessionID:           escrow.SessionID,
			session:             escrow.Session,
			snapshots:           s.snapshots,
			predicates:          s.predicates(escrow),
			acquireSlot:         s.acquireSlot(escrow),
			holdEscrow:          escrow.Hold,
			escalationHold:      escrow.EscalationHold,
			observer:            s.observer,
			now:                 s.now,
			matchWait:           s.matchWait(),
			maxConsecutiveBurns: s.maxConsecutiveBurns,
			retirementReserve:   s.retirementReserveFor(escrow),
			newTimer:            s.newTimer,
			retire:              s.retire,
			idleGrace:           idleDispatcherGrace,
			submitBuffer:        s.dispatchQueueDepth(),
			onExhausted:         s.onEscrowExhausted,
		})
		s.dispatchers[escrow.ID] = target
		target.start()
	}
	target.pendingSubmits.Add(1)
	return target, nil
}

func (s *Scheduler) retire(idle *dispatcher) bool {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	if idle.pendingSubmits.Load() != 0 || !idle.markStopped() {
		return false
	}
	delete(s.superseded, idle)
	if s.dispatchers[idle.escrowID] != idle {
		return true
	}
	delete(s.dispatchers, idle.escrowID)
	if s.observer != nil {
		s.observer.EscrowRetired(idle.escrowID)
	}
	return true
}

// supersede keeps a dispatcher a new session of its escrow replaces owned until it idles out, so Stop waits for it and the waiters it already holds are still served. Caller holds registryMu.
func (s *Scheduler) supersede(previous *dispatcher) {
	if s.superseded == nil {
		s.superseded = make(map[*dispatcher]struct{})
	}
	s.superseded[previous] = struct{}{}
}
