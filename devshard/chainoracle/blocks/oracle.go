package blocks

import "context"

// BlockOracle is the stable contract between producers (observers, the
// standalone binary, the in-process dapi mount) and consumers (devshardd
// hosts, real dapi internals).
//
// All implementations MUST return pre-verified headers; consumers that
// ingest a header are expected to re-verify locally as defence-in-depth,
// but they are not required to re-prove commit signatures on every access.
type BlockOracle interface {
	Latest(ctx context.Context) (*Header, error)
	At(ctx context.Context, height int64) (*Header, error)
	Prove(ctx context.Context, path string, height int64) (*Proof, error)
	Subscribe(ctx context.Context, fromHeight int64) (<-chan *Header, error)
}
