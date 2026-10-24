package storage

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"devshard/types"
)

const (
	// DiffPageMaxNonces is the most nonces one page may carry.
	DiffPageMaxNonces = 64
	// DiffPageMaxBytes is the most stored txs_proto bytes one page may carry.
	// A single diff larger than this is a page by itself.
	DiffPageMaxBytes = 8 << 20
)

// ErrDiffPageLimit is a requested range wider than one page. The text names
// both caps so an HTTP 400 can be recognized without a second code.
var ErrDiffPageLimit error = diffPageLimitError{}

type diffPageLimitError struct{}

func (diffPageLimitError) Error() string {
	return fmt.Sprintf(
		"diff range exceeds one page (at most %d nonces and %d bytes)",
		DiffPageMaxNonces, DiffPageMaxBytes,
	)
}

// DiffSize is one stored nonce and the length of its stored txs_proto.
type DiffSize struct {
	Nonce uint64
	Bytes int
}

// DiffReader is the subset of Storage that ReadDiffPages walks.
type DiffReader interface {
	GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error)
	// DiffSizes lists stored nonces in [fromNonce, toNonce] in ascending
	// order, at most limit of them, with the stored txs_proto length. It does
	// not decode transactions. The first entry is the lowest stored nonce at
	// or above fromNonce, so a hole is resolved by the same read.
	DiffSizes(escrowID string, fromNonce, toNonce uint64, limit int) ([]DiffSize, error)
}

// DiffGapError is a hole or a missing tail in [Expected, To].
// Next is the next stored nonce when one exists; it is 0 when the range ends.
type DiffGapError struct {
	Expected uint64
	Next     uint64
	To       uint64
}

func (e *DiffGapError) Error() string {
	if e == nil {
		return "diff nonce gap"
	}
	if e.Next == 0 {
		return fmt.Sprintf("missing trailing nonces %d..%d", e.Expected, e.To)
	}
	return fmt.Sprintf("missing nonce %d, next stored nonce is %d", e.Expected, e.Next)
}

// ReadDiffPages visits [from, to] in contiguous pages and drops each page
// when fn returns. A page is at most DiffPageMaxNonces nonces and
// DiffPageMaxBytes of stored txs_proto. fn must not retain the slice.
//
// Each page costs one DiffSizes read and one GetDiffs read of exactly that
// page. A hole delivers the contiguous prefix, then returns *DiffGapError
// unless fn already returned an error. from > to does not call the store.
func ReadDiffPages(store DiffReader, escrowID string, from, to uint64, fn func([]types.DiffRecord) error) error {
	for n := from; n <= to; {
		sizes, err := store.DiffSizes(escrowID, n, to, DiffPageMaxNonces)
		if err != nil {
			return fmt.Errorf("get diff sizes: %w", err)
		}
		if len(sizes) == 0 {
			return &DiffGapError{Expected: n, To: to}
		}
		if sizes[0].Nonce != n {
			return &DiffGapError{Expected: n, Next: sizes[0].Nonce, To: to}
		}
		count := packDiffPage(sizes)
		last := sizes[count-1].Nonce
		recs, err := store.GetDiffs(escrowID, n, last)
		if err != nil {
			return fmt.Errorf("get diffs: %w", err)
		}
		page, gap := contiguousDiffs(recs, n, last, to)
		if len(page) > 0 {
			if err := fn(page); err != nil {
				return err
			}
		}
		if gap != nil {
			return gap
		}
		if last == to {
			return nil
		}
		if count < len(sizes) {
			if next := sizes[count].Nonce; next != last+1 {
				return &DiffGapError{Expected: last + 1, Next: next, To: to}
			}
		} else if len(sizes) < DiffPageMaxNonces {
			return &DiffGapError{Expected: last + 1, To: to}
		}
		n = last + 1
	}
	return nil
}

// packDiffPage returns how many leading entries of sizes form one page: a
// contiguous run of at most DiffPageMaxNonces nonces within DiffPageMaxBytes.
// The first entry is always taken, so an oversized diff is a page by itself.
func packDiffPage(sizes []DiffSize) int {
	total := 0
	for i, s := range sizes {
		if i == DiffPageMaxNonces {
			return i
		}
		if i > 0 && (s.Nonce != sizes[i-1].Nonce+1 || total+s.Bytes > DiffPageMaxBytes) {
			return i
		}
		total += s.Bytes
	}
	return len(sizes)
}

// contiguousDiffs keeps the run of recs that starts at from and steps by one
// through last. A record missing from that run is a *DiffGapError over
// [missing, to].
func contiguousDiffs(recs []types.DiffRecord, from, last, to uint64) ([]types.DiffRecord, *DiffGapError) {
	expect := from
	for i, rec := range recs {
		if rec.Nonce != expect {
			next := uint64(0)
			if rec.Nonce > expect {
				next = rec.Nonce
			}
			return recs[:i], &DiffGapError{Expected: expect, Next: next, To: to}
		}
		if expect == last {
			return recs[:i+1], nil
		}
		expect++
	}
	return recs, &DiffGapError{Expected: expect, To: to}
}

func diffTxsProtoSize(rec types.DiffRecord) int {
	return proto.Size(&types.DiffContent{Txs: rec.Txs})
}

// LoadBoundedDiffs returns every stored diff in [from, to] when that request
// is one page. Missing nonces are skipped. A wider request returns
// ErrDiffPageLimit before any transaction is decoded. A single diff larger
// than DiffPageMaxBytes is that one page.
func LoadBoundedDiffs(store DiffReader, escrowID string, from, to uint64) ([]types.DiffRecord, error) {
	if from > to {
		return nil, nil
	}
	if to-from >= DiffPageMaxNonces {
		return nil, ErrDiffPageLimit
	}
	sizes, err := store.DiffSizes(escrowID, from, to, DiffPageMaxNonces)
	if err != nil {
		return nil, fmt.Errorf("get diff sizes: %w", err)
	}
	if len(sizes) == 0 {
		return nil, nil
	}
	total := 0
	for _, s := range sizes {
		total += s.Bytes
	}
	if len(sizes) > 1 && total > DiffPageMaxBytes {
		return nil, ErrDiffPageLimit
	}
	recs, err := store.GetDiffs(escrowID, sizes[0].Nonce, sizes[len(sizes)-1].Nonce)
	if err != nil {
		return nil, fmt.Errorf("get diffs: %w", err)
	}
	return recs, nil
}
