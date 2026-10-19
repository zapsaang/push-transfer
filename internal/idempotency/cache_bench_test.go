package idempotency

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkMemoryCache_CheckAndRecord_Serial(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 10*time.Minute, 1000000)
	defer cache.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := "key-" + strconv.Itoa(i)
		_ = cache.CheckAndRecord(key)
	}
}

func BenchmarkMemoryCache_CheckAndRecord_Parallel(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 10*time.Minute, 1000000)
	defer cache.Close()

	var counter int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			val := atomic.AddInt64(&counter, 1)
			key := "par-key-" + strconv.FormatInt(val, 10)
			_ = cache.CheckAndRecord(key)
		}
	})
}

func BenchmarkMemoryCache_CheckAndRecord_DuplicateHits(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 10*time.Minute, 1000000)
	defer cache.Close()

	// Pre-seed the cache
	for i := 0; i < 1000; i++ {
		cache.CheckAndRecord(fmt.Sprintf("fixed-key-%d", i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var idx int
		for pb.Next() {
			idx++
			key := fmt.Sprintf("fixed-key-%d", idx%1000)
			_ = cache.CheckAndRecord(key)
		}
	})
}

func BenchmarkMemoryCache_Eviction(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Max keys set to 500 to trigger evictions frequently
	cache := NewMemoryCache(ctx, 10*time.Minute, 500)
	defer cache.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := "evict-" + strconv.Itoa(i)
		_ = cache.CheckAndRecord(key)
	}
}
