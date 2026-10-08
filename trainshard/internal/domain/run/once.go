package run

import (
	"context"

	"trainshard/internal/utils/syncx"
)

// Once holds the request's lock from lookup to record, so a repeat that arrives while the first is
// still applying waits for that answer. A refusal of the whole request is returned, not recorded
type Once struct {
	log      RequestLog
	applying syncx.Keyed[RequestRef]
}

func NewOnce(log RequestLog) *Once {
	return &Once{log: log}
}

func (o *Once) Do(ctx context.Context, ref RequestRef, apply func(context.Context) ([]NodeResult, error)) ([]NodeResult, error) {
	defer o.applying.Lock(ref)()

	recorded, found, err := o.log.Result(ctx, ref)
	if err != nil || found {
		return recorded, err
	}
	results, err := apply(ctx)
	if err != nil {
		return nil, err
	}
	if err := o.log.Record(ctx, ref, results); err != nil {
		return nil, err
	}
	return results, nil
}
