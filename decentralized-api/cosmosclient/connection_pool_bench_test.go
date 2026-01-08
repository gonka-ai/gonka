package cosmosclient

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	igniteclient "github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
)

func TestConnectionPoolPerformance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	single := &igniteclient.Client{}
	var mu sync.Mutex

	pool := &ConnectionPool{
		clients: []*igniteclient.Client{{}, {}, {}, {}, {}},
		healthy: makeHealthySlice(true, true, true, true, true),
		ctx:     ctx,
		cancel:  cancel,
	}

	iterations := 1_000_000
	goroutines := 100

	fmt.Println("\n=== Connection Pool Performance Test ===")
	fmt.Printf("Iterations: %d, Goroutines: %d\n\n", iterations, goroutines)

	// Test 1: Single connection with mutex
	var singleOps int64
	start := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations/goroutines; i++ {
				mu.Lock()
				_ = single
				mu.Unlock()
				atomic.AddInt64(&singleOps, 1)
			}
		}()
	}
	wg.Wait()
	singleDuration := time.Since(start)

	// Test 2: Pool with 5 connections
	var poolOps int64
	start = time.Now()
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations/goroutines; i++ {
				pool.Get()
				atomic.AddInt64(&poolOps, 1)
			}
		}()
	}
	wg.Wait()
	poolDuration := time.Since(start)

	fmt.Println("Results:")
	fmt.Println("----------------------------------------")
	fmt.Printf("Single connection + mutex:  %v\n", singleDuration)
	fmt.Printf("Pool (5 connections):       %v\n", poolDuration)
	fmt.Println("----------------------------------------")
	fmt.Printf("Operations per second (single): %.0f ops/sec\n", float64(singleOps)/singleDuration.Seconds())
	fmt.Printf("Operations per second (pool):   %.0f ops/sec\n", float64(poolOps)/poolDuration.Seconds())
	fmt.Println("----------------------------------------")

	if poolDuration < singleDuration {
		fmt.Printf("Pool is %.1fx FASTER\n", float64(singleDuration)/float64(poolDuration))
	} else {
		fmt.Printf("Pool overhead: +%v (acceptable for resilience benefits)\n", poolDuration-singleDuration)
	}
	fmt.Println()
}
