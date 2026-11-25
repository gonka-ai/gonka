package poc

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"common/logging"
	"decentralized-api/broker"

	"github.com/productscience/inference/x/inference/types"
)

// VerdictTracker follows the validation jobs DAPI hands to its ML nodes until
// their /validated callback arrives. An ML node that fails a job sends no
// callback, and DAPI used to forget a job as soon as the node accepted it, so
// a lost verdict left no trace and was never sent again.
type VerdictTracker struct {
	mu     sync.Mutex
	stages map[int64]*verdictStage
}

// Verdicts is shared by the validator, which records dispatches, and the
// /validated callback handler, which records answers.
var Verdicts = NewVerdictTracker()

func NewVerdictTracker() *VerdictTracker {
	return &VerdictTracker{stages: make(map[int64]*verdictStage)}
}

type verdictKey struct {
	address string
	modelID string
}

// dispatchContext is what dispatchToMLNode needs to send a job again. Each job
// keeps its own: a challenge validates with its own seed, frozen recipe and node
// list, and its start height can equal a regular stage's.
type dispatchContext struct {
	nodes     []broker.NodeResponse
	startHash string
	params    *types.PocParams
}

type verdictJob struct {
	work         participantWork
	dc           dispatchContext
	nodeNum      uint64 // node holding the job now
	sentAt       time.Time
	redispatched bool // sent to a second node
	retryTried   bool // a re-dispatch was attempted, sent or not
	answered     bool
	answeredAt   time.Time
}

type nodeVerdicts struct {
	host     string
	sent     int
	answered int
}

type verdictStage struct {
	jobs  map[verdictKey]*verdictJob
	nodes map[uint64]*nodeVerdicts
}

func (s *verdictStage) node(nodeNum uint64) *nodeVerdicts {
	n := s.nodes[nodeNum]
	if n == nil {
		n = &nodeVerdicts{}
		s.nodes[nodeNum] = n
	}
	return n
}

// VerdictStats summarizes one stage.
type VerdictStats struct {
	Dispatched         int
	Answered           int
	Redispatched       int
	AnsweredRedispatch int
	Lost               int
	AnswerTimeP50      time.Duration
	AnswerTimeMax      time.Duration
	LostByNode         map[string]int // "host (node N)" -> jobs still without a verdict
}

// BeginStage starts tracking a stage and reports whether this call started it,
// so a stage validated by several passes gets one watcher. Sent and Answered
// for a stage that was not begun are ignored.
func (t *VerdictTracker) BeginStage(height int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.stages[height]; ok {
		return false
	}
	t.stages[height] = &verdictStage{
		jobs:  make(map[verdictKey]*verdictJob),
		nodes: make(map[uint64]*nodeVerdicts),
	}
	return true
}

// EndStage drops a stage.
func (t *VerdictTracker) EndStage(height int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.stages, height)
}

// Sent records that a node accepted a job. A second send of the same job is a re-dispatch.
func (t *VerdictTracker) Sent(height int64, work participantWork, dc dispatchContext, nodeNum uint64, host string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	stage, ok := t.stages[height]
	if !ok {
		return
	}
	key := verdictKey{address: work.address, modelID: work.modelId}
	job, ok := stage.jobs[key]
	switch {
	case !ok:
		job = &verdictJob{}
		stage.jobs[key] = job
	case !job.sentAt.IsZero():
		job.redispatched = true
	}
	job.work = work
	job.dc = dc
	job.nodeNum = nodeNum
	job.sentAt = now
	n := stage.node(nodeNum)
	n.host = host
	n.sent++
}

// Answered records a /validated callback. nodeNum is the node_id the ML node
// echoes back; 0 means unknown, and the answer is credited to the job's node.
func (t *VerdictTracker) Answered(height int64, address, modelID string, nodeNum uint64, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	stage, ok := t.stages[height]
	if !ok {
		return
	}
	key := verdictKey{address: address, modelID: modelID}
	job, ok := stage.jobs[key]
	if !ok {
		// The callback can beat Sent, which runs after the node's HTTP reply.
		job = &verdictJob{}
		stage.jobs[key] = job
	}
	if nodeNum == 0 {
		nodeNum = job.nodeNum
	}
	if nodeNum != 0 {
		stage.node(nodeNum).answered++
	}
	if !job.answered {
		job.answered = true
		job.answeredAt = now
	}
}

// overdue returns the jobs that have waited at least timeout for a verdict and
// have not been re-dispatched yet, and marks them so each job is retried once.
func (t *VerdictTracker) overdue(height int64, now time.Time, timeout time.Duration) []verdictJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	stage, ok := t.stages[height]
	if !ok || timeout <= 0 {
		return nil
	}
	var out []verdictJob
	for _, job := range stage.jobs {
		if job.answered || job.retryTried || job.sentAt.IsZero() || now.Sub(job.sentAt) < timeout {
			continue
		}
		job.retryTried = true
		out = append(out, *job)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sentAt.Before(out[j].sentAt) })
	return out
}

// pickNode returns the index in modelNodes of the node to re-dispatch to: not
// the node that lost the job, one that has answered in this stage, and the one
// with the fewest jobs still waiting.
func (t *VerdictTracker) pickNode(height int64, modelNodes []broker.NodeResponse, exclude uint64) (int, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	stage, ok := t.stages[height]
	if !ok {
		return 0, false
	}
	best, bestWaiting := -1, 0
	for i, nr := range modelNodes {
		num := nr.Node.NodeNum
		n := stage.nodes[num]
		if num == exclude || n == nil || n.answered == 0 {
			continue
		}
		waiting := n.sent - n.answered
		if best < 0 || waiting < bestWaiting {
			best, bestWaiting = i, waiting
		}
	}
	return best, best >= 0
}

// Stats summarizes a stage.
func (t *VerdictTracker) Stats(height int64) VerdictStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	stats := VerdictStats{LostByNode: make(map[string]int)}
	stage, ok := t.stages[height]
	if !ok {
		return stats
	}
	var times []time.Duration
	for _, job := range stage.jobs {
		if job.answered {
			stats.Answered++
			if job.redispatched {
				stats.AnsweredRedispatch++
			}
			if !job.sentAt.IsZero() && job.answeredAt.After(job.sentAt) {
				times = append(times, job.answeredAt.Sub(job.sentAt))
			}
		}
		if job.sentAt.IsZero() {
			continue
		}
		stats.Dispatched++
		if job.redispatched {
			stats.Redispatched++
		}
		if !job.answered {
			stats.Lost++
			name := fmt.Sprintf("node %d", job.nodeNum)
			if n := stage.nodes[job.nodeNum]; n != nil && n.host != "" {
				name = fmt.Sprintf("%s (node %d)", n.host, job.nodeNum)
			}
			stats.LostByNode[name]++
		}
	}
	if len(times) > 0 {
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		stats.AnswerTimeP50 = times[len(times)/2]
		stats.AnswerTimeMax = times[len(times)-1]
	}
	return stats
}

// watchVerdicts runs for one stage until its validation window closes. It sends
// a job with no verdict after VerdictTimeout once to another node, then logs
// how many jobs got a verdict and which nodes lost the rest.
func (v *OffChainValidator) watchVerdicts(pocHeight int64, challenge bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v.cancelWhenValidationPhaseEnds(ctx, cancel, pocHeight, challenge)

	interval := v.config.VerdictCheckInterval
	if interval <= 0 {
		interval = DefaultValidationConfig().VerdictCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			v.logVerdicts(pocHeight)
			v.verdicts.EndStage(pocHeight)
			return
		case now := <-ticker.C:
			v.redispatchOverdue(ctx, pocHeight, now)
		}
	}
}

// redispatchOverdue sends every job that has waited VerdictTimeout without a
// verdict to another node of its model that has answered in this stage. A job
// is re-dispatched at most once, with the seed, recipe and nodes it was first
// sent with; with no such node it stays lost and shows up in the stage summary.
func (v *OffChainValidator) redispatchOverdue(ctx context.Context, pocHeight int64, now time.Time) {
	for _, job := range v.verdicts.overdue(pocHeight, now, v.config.VerdictTimeout) {
		work := job.work
		modelNodes := filterValidationNodesForModel(job.dc.nodes, work.modelId)
		idx, ok := v.verdicts.pickNode(pocHeight, modelNodes, job.nodeNum)
		if !ok {
			logging.Debug("OffChainValidator: no verdict from ML node and no other node answering, not re-dispatching", types.PoC,
				"participant", work.address, "modelId", work.modelId, "nodeNum", job.nodeNum,
				"waited", now.Sub(job.sentAt).Round(time.Second))
			continue
		}
		logging.Warn("OffChainValidator: no verdict from ML node, re-dispatching", types.PoC,
			"participant", work.address, "modelId", work.modelId, "nodeNum", job.nodeNum,
			"to", modelNodes[idx].Node.Host, "waited", now.Sub(job.sentAt).Round(time.Second))
		// dispatchToMLNode picks modelNodes[counter % len], so the counter selects the node.
		counter := idx
		if result := v.dispatchToMLNode(ctx, &work, job.dc.nodes, &counter, pocHeight, job.dc.startHash, job.dc.params); result != validateDispatched {
			logging.Warn("OffChainValidator: re-dispatch failed", types.PoC,
				"participant", work.address, "modelId", work.modelId, "to", modelNodes[idx].Node.Host)
		}
	}
}

func (v *OffChainValidator) logVerdicts(pocHeight int64) {
	stats := v.verdicts.Stats(pocHeight)
	if stats.Dispatched == 0 {
		return
	}
	args := []interface{}{
		"pocStageStartBlockHeight", pocHeight,
		"dispatched", stats.Dispatched,
		"answered", stats.Answered,
		"redispatched", stats.Redispatched,
		"answeredAfterRedispatch", stats.AnsweredRedispatch,
		"lost", stats.Lost,
		"answerTimeP50", stats.AnswerTimeP50.Round(time.Second),
		"answerTimeMax", stats.AnswerTimeMax.Round(time.Second),
	}
	if stats.Lost == 0 {
		logging.Info("OffChainValidator: ML node verdicts", types.PoC, args...)
		return
	}
	args = append(args, "lostByNode", stats.LostByNode)
	logging.Warn("OffChainValidator: ML node verdicts, some jobs got no verdict", types.PoC, args...)
}
