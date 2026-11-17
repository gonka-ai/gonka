package storage

import (
	"errors"
	"fmt"

	"devshard/types"
)

// ErrHostAdmissionGap prevents an unchecked durable prefix being blessed by
// appending a fresh admitted tail. Missing marks require explicit revalidation.
var ErrHostAdmissionGap = errors.New("host admission prefix gap")

// hostAdmissionRange returns the contiguous run explicitly admitted by a host.
// Ordinary gateway writes never create or advance the host watermark.
func hostAdmissionRange(recs []types.DiffRecord) (first, last uint64, err error) {
	if len(recs) == 0 {
		return
	}
	admitted := recs[0].HostAdmitted
	for _, rec := range recs {
		if rec.HostAdmitted != admitted {
			return 0, 0, fmt.Errorf("%w: mixed admission batch", ErrHostAdmissionGap)
		}
		if !rec.HostAdmitted {
			continue
		}
		if first == 0 {
			first = rec.Nonce
		} else if rec.Nonce != last+1 {
			return 0, 0, fmt.Errorf("%w: noncontiguous admission batch", ErrHostAdmissionGap)
		}
		if rec.Nonce == 0 {
			return 0, 0, fmt.Errorf("%w: zero nonce", ErrHostAdmissionGap)
		}
		last = rec.Nonce
	}
	return
}
