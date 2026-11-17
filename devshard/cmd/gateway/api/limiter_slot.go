package api

import "sync/atomic"

type limiterSlot struct {
	holders atomic.Int32
	release func()
}

func newLimiterSlot(release func()) *limiterSlot {
	slot := &limiterSlot{release: release}
	slot.holders.Store(1)
	return slot
}

func (s *limiterSlot) share() func() {
	s.holders.Add(1)
	return s.done
}

func (s *limiterSlot) done() {
	if s.holders.Add(-1) == 0 {
		s.release()
	}
}
