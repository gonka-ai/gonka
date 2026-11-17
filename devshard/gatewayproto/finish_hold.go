package gatewayproto

import "sync/atomic"

const (
	finishUntaken int32 = iota
	finishTaken
	finishReleased
)

// FinishHold tracks a single retained Finish across a sender-to-claim handoff.
// Share its pointer when copying evidence; the atomic ownership must not be copied.
type FinishHold struct {
	release   func()
	ownership atomic.Int32
}

func NewFinishHold(release func()) *FinishHold { return &FinishHold{release: release} }

func (h *FinishHold) Take() {
	if h != nil {
		h.ownership.CompareAndSwap(finishUntaken, finishTaken)
	}
}

func (h *FinishHold) Release() {
	if h != nil && h.release != nil && h.ownership.Swap(finishReleased) != finishReleased {
		h.release()
	}
}

func (h *FinishHold) ReleaseUntaken() {
	if h != nil && h.release != nil && h.ownership.CompareAndSwap(finishUntaken, finishReleased) {
		h.release()
	}
}
