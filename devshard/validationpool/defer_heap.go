package validationpool

import (
	"container/heap"
	"time"
)

type deferredItem struct {
	job        Job
	eligibleAt time.Time
	index      int
}

type deferHeap []*deferredItem

func (h deferHeap) Len() int { return len(h) }

func (h deferHeap) Less(i, j int) bool {
	return h[i].eligibleAt.Before(h[j].eligibleAt)
}

func (h deferHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *deferHeap) Push(x any) {
	item := x.(*deferredItem)
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *deferHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[:n-1]
	return item
}

func (h *deferHeap) peek() *deferredItem {
	if len(*h) == 0 {
		return nil
	}
	return (*h)[0]
}

// dropEscrow removes deferred jobs for escrowID and returns them.
func (h *deferHeap) dropEscrow(escrowID string) []Job {
	var out []Job
	kept := (*h)[:0]
	for _, it := range *h {
		if it.job.EscrowID == escrowID {
			out = append(out, it.job)
			continue
		}
		kept = append(kept, it)
	}
	*h = kept
	for i := range *h {
		(*h)[i].index = i
	}
	if len(*h) > 0 {
		heap.Init(h)
	}
	return out
}

func (h *deferHeap) drain() []Job {
	out := make([]Job, 0, len(*h))
	for _, it := range *h {
		out = append(out, it.job)
	}
	*h = nil
	return out
}
