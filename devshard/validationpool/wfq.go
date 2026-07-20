package validationpool

// wfq is a deficit round-robin weighted fair queue keyed by EscrowID.
// Packet size is 1 (one job). A non-empty bucket is credited Weight when a
// visit begins and keeps serving on subsequent Pop calls until deficit is
// exhausted or the bucket empties. Empty buckets are pruned from order so
// Pop stays O(active escrows), not O(ever offered since last ForgetHost).
type wfq struct {
	order   []string // escrow visit order
	buckets map[string]*wfqBucket
	next    int
	len     int
}

type wfqBucket struct {
	escrowID string
	weight   uint32
	deficit  uint32
	jobs     []Job
}

func newWFQ() *wfq {
	return &wfq{buckets: make(map[string]*wfqBucket)}
}

func (q *wfq) Len() int { return q.len }

func (q *wfq) Push(job Job) {
	w := job.Weight
	if w == 0 {
		w = 1
	}
	b, ok := q.buckets[job.EscrowID]
	if !ok {
		b = &wfqBucket{escrowID: job.EscrowID, weight: w}
		q.buckets[job.EscrowID] = b
		q.order = append(q.order, job.EscrowID)
	} else {
		b.weight = w // refresh on Offer
	}
	b.jobs = append(b.jobs, job)
	q.len++
}

// Pop removes one job using DRR. ok is false when empty.
func (q *wfq) Pop() (Job, bool) {
	if q.len == 0 {
		return Job{}, false
	}
	for attempts := 0; attempts < len(q.order)+1; attempts++ {
		if len(q.order) == 0 {
			return Job{}, false
		}
		if q.next >= len(q.order) {
			q.next = 0
		}
		id := q.order[q.next]
		b := q.buckets[id]
		if b == nil || len(b.jobs) == 0 {
			// Sweep lingering empty / missing buckets.
			q.removeAt(q.next)
			continue
		}
		if b.deficit == 0 {
			b.deficit = b.weight
			if b.deficit == 0 {
				b.deficit = 1
			}
		}
		job := b.jobs[0]
		b.jobs = b.jobs[1:]
		b.deficit--
		q.len--
		if len(b.jobs) == 0 {
			b.deficit = 0
			q.removeAt(q.next) // next now points at the following escrow
		} else if b.deficit == 0 {
			q.next++
		}
		// else stay on this bucket for the next Pop
		return job, true
	}
	return Job{}, false
}

// removeAt deletes order[i] and its bucket. After removal, next is left at i
// (the former i+1 slides into place), or wrapped to 0 if past the end.
func (q *wfq) removeAt(i int) {
	id := q.order[i]
	delete(q.buckets, id)
	q.order = append(q.order[:i], q.order[i+1:]...)
	if q.next > i {
		q.next--
	}
	if q.next >= len(q.order) {
		q.next = 0
	}
}

// DropEscrow removes all pending jobs for escrowID and returns them.
func (q *wfq) DropEscrow(escrowID string) []Job {
	b, ok := q.buckets[escrowID]
	if !ok {
		return nil
	}
	out := b.jobs
	q.len -= len(out)
	for i, id := range q.order {
		if id == escrowID {
			q.removeAt(i)
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Drain removes all pending jobs.
func (q *wfq) Drain() []Job {
	if q.len == 0 {
		return nil
	}
	out := make([]Job, 0, q.len)
	for _, id := range q.order {
		b := q.buckets[id]
		if b == nil {
			continue
		}
		out = append(out, b.jobs...)
	}
	q.order = nil
	q.buckets = make(map[string]*wfqBucket)
	q.next = 0
	q.len = 0
	return out
}
