package api

import (
	"context"

	"trainshard/internal/domain/shard"
	"trainshard/internal/utils/signedhttp"
)

func actorFrom(ctx context.Context) shard.Actor {
	return shard.Actor{Address: signedhttp.AddressFrom(ctx)}
}

func requestIDFrom(ctx context.Context) string {
	return signedhttp.RequestIDFrom(ctx)
}
