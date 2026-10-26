package harness

// Devshard runs an inference's timeouts on block time: refusal_timeout from the height-sync floor as
// of the diff holding its Start, execution_timeout from the floor as of the diff holding its
// ConfirmStart, each dated by the time of that block's header (state/anchor.go). The diffs carry the
// heights, not the times: the Start's stamp is the floor (the state machine rejects any other), and
// the floor as of a ConfirmStart is folded here from the diffs before it the way the state machine
// folds it on apply. The times come from the chain's headers, which the export keeps.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"devshard/heightsync"
	"devshard/types"
)

// ChainHeadersName is the export file of the chain's block headers: their heights, hashes and times.
const ChainHeadersName = "chain-headers.json"

// ChainHeader is one block header of the chain: its hash (hex) and time in unix milliseconds.
type ChainHeader struct {
	Height uint64 `json:"height"`
	Hash   string `json:"hash"`
	TimeMs int64  `json:"time_ms"`
}

// ChainHeaders are the chain's block headers by height.
type ChainHeaders map[uint64]ChainHeader

// ReadChainHeaders reads the export's block headers.
func ReadChainHeaders(exportDir string) (ChainHeaders, error) {
	raw, err := os.ReadFile(filepath.Join(exportDir, ChainHeadersName))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Headers []ChainHeader `json:"headers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", ChainHeadersName, err)
	}
	out := ChainHeaders{}
	for _, h := range doc.Headers {
		out[h.Height] = h
	}
	return out, nil
}

// HeaderTimeMs serves the headers to a state machine replaying the chain (state.HeaderTimeSource):
// a height the export does not hold, or a hash other than its block's, is a header still pending.
func (h ChainHeaders) HeaderTimeMs(height uint64, hash []byte) (int64, error) {
	hdr, ok := h[height]
	if !ok || hdr.TimeMs <= 0 {
		return 0, fmt.Errorf("%w: height %d is not in the export", types.ErrHeaderPending, height)
	}
	if want, err := hex.DecodeString(hdr.Hash); err != nil || !bytes.Equal(want, hash) {
		return 0, fmt.Errorf("%w: height %d has another hash in the export", types.ErrHeaderPending, height)
	}
	return hdr.TimeMs, nil
}

// HeaderTimes gives the time of the block at a height in unix milliseconds, ok false when unknown.
type HeaderTimes func(height uint64) (ms int64, ok bool)

// Times reads the headers as HeaderTimes.
func (h ChainHeaders) Times(height uint64) (int64, bool) {
	hdr, ok := h[height]
	return hdr.TimeMs, ok && hdr.TimeMs > 0
}

// DateAnchors sets StartedAt and ConfirmedAt of every inference from the times of its anchor heights
// and returns how many anchor heights it could not date. An anchor at height 0 (the floor was empty,
// the timeout is not armed) has no time.
func (d ExportDiffs) DateAnchors(times HeaderTimes) (undated int) {
	date := func(height uint64) int64 {
		if height == 0 {
			return 0
		}
		ms, ok := times(height)
		if !ok {
			undated++
			return 0
		}
		return ms / 1000
	}
	for _, chain := range d.Hosts {
		for _, f := range chain.Inferences {
			if f.Started {
				f.StartedAt = date(f.StartedHeight)
			}
			if f.Confirmed {
				f.ConfirmedAt = date(f.ConfirmedHeight)
			}
		}
	}
	return undated
}

// escrowFloor is what folding one escrow's floor needs from its diffs: their reference-height
// claims and the diff of every ConfirmStart.
type escrowFloor struct {
	claims  map[uint64][]heightsync.FloorClaim
	confirm map[uint64]uint64 // inference id → nonce of the diff holding its ConfirmStart
}

// observeFloor records one diff's claims as state.floorClaims names them: a stamp claims its height
// for the slot that signed it, the gateway's (Start, heartbeat) for the sequencer, which never raises
// the floor. A ConfirmStart's signer is the executor of record; any slot claims the same height.
func (c *HostChain) observeFloor(escrow string, nonce uint64, txs []*types.DevshardTx) {
	if c.floors == nil {
		c.floors = map[string]*escrowFloor{}
	}
	e := c.floors[escrow]
	if e == nil {
		e = &escrowFloor{claims: map[uint64][]heightsync.FloorClaim{}, confirm: map[uint64]uint64{}}
		c.floors[escrow] = e
	}
	for _, tx := range txs {
		if m := tx.GetConfirmStart(); m != nil {
			e.confirm[m.InferenceId] = nonce
		}
		h, hash, ok := heightsync.RefStamp(tx)
		if !ok {
			continue
		}
		signer := heightsync.SequencerSigner
		switch {
		case tx.GetHeightAck() != nil:
			signer = tx.GetHeightAck().SlotId
		case tx.GetFinishInference() != nil:
			signer = tx.GetFinishInference().ExecutorSlot
		case tx.GetConfirmStart() != nil:
			signer = 0
		}
		e.claims[nonce] = append(e.claims[nonce], heightsync.FloorClaim{Signer: signer, Height: h, Hash: hash})
	}
}

// foldFloors sets ConfirmedHeight of every confirmed inference: the floor as of its ConfirmStart's diff.
func (c *HostChain) foldFloors() {
	for escrow, e := range c.floors {
		nonces := make([]uint64, 0, len(e.claims))
		for n := range e.claims {
			nonces = append(nonces, n)
		}
		sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })
		floor := heightsync.NewFloorIndexWith(heightsync.FloorConfig{Window: len(nonces) + 1})
		for _, n := range nonces {
			floor.Observe(n, e.claims[n])
		}
		for id, n := range e.confirm {
			if f := c.Inferences[InferenceRef{Escrow: escrow, Inference: id}]; f != nil {
				if p, known := floor.PointAsOf(n); known {
					f.ConfirmedHeight = p.Height
				}
			}
		}
	}
	c.floors = nil
}
