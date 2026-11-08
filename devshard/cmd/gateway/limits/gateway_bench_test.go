package limits

import (
	"context"
	"testing"
)

func benchCapacity() ModelCapacity {
	return ModelCapacity{ScaleFactor: 1, CurrentWeight: 1000, BaselineWeight: 1000, MaxConcurrentPer10000Weight: 0}
}

func BenchmarkLimiterAcquireRelease(b *testing.B) {
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1_000_000, MaxInputTokens: 1_000_000_000})
	capacity := benchCapacity()
	ctx := context.Background()
	b.ReportAllocs()
	for range b.N {
		if err := limiter.AcquireForModel(ctx, "model-0", 100, capacity); err != nil {
			b.Fatal(err)
		}
		limiter.ReleaseForModel("model-0", 100)
	}
}

func BenchmarkLimiterAcquireReleaseParallel(b *testing.B) {
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1_000_000, MaxInputTokens: 1_000_000_000})
	capacity := benchCapacity()
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := limiter.AcquireForModel(ctx, "model-0", 100, capacity); err != nil {
				b.Fatal(err)
			}
			limiter.ReleaseForModel("model-0", 100)
		}
	})
}
