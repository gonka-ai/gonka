package run

import (
	"context"

	"trainshard/internal/utils/syncx"
)

// Once answers a request one time: the request is looked up first and recorded last, under a lock
// on its id, so a repeat that arrives while the first is still applying waits for that answer
type Once struct {
	log      RequestLog
	applying syncx.Keyed[RequestRef]
}

func NewOnce(log RequestLog) *Once {
	return &Once{log: log}
}

func (o *Once) Do(ctx context.Context, ref RequestRef, apply func(context.Context) []NodeResult) ([]NodeResult, error) {
	defer o.applying.Lock(ref)()

	recorded, found, err := o.log.Result(ctx, ref)
	if err != nil || found {
		return recorded, err
	}
	results := apply(ctx)
	if err := o.log.Record(ctx, ref, results); err != nil {
		return nil, err
	}
	return results, nil
}
