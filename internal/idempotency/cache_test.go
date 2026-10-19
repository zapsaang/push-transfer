package idempotency

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryCache_BasicDeduplication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 10*time.Second, 100)
	defer cache.Close()

	key := "test-order-001"

	// 首次调用放行返回 true
	if !cache.CheckAndRecord(key) {
		t.Fatalf("first call for %q should return true (allowed)", key)
	}

	// TTL 内二次调用返回 false
	if cache.CheckAndRecord(key) {
		t.Fatalf("second call for %q within TTL should return false (duplicate)", key)
	}

	// 不同的 key 应该被放行
	if !cache.CheckAndRecord("different-key") {
		t.Fatalf("call with different key should return true")
	}
}

func TestMemoryCache_EmptyKeyAlwaysAllowed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 10*time.Second, 100)
	defer cache.Close()

	// 空 key 连续调用均恒返回 true（不进行去重）
	for i := 0; i < 5; i++ {
		if !cache.CheckAndRecord("") {
			t.Fatalf("call %d with empty key should return true", i)
		}
	}
}

func TestMemoryCache_WhitespaceKeyAlwaysAllowed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 10*time.Second, 100)
	defer cache.Close()

	whitespaceKeys := []string{" ", "   ", "\t", "\n", " \t \r\n "}
	for _, ws := range whitespaceKeys {
		for i := 0; i < 3; i++ {
			if !cache.CheckAndRecord(ws) {
				t.Fatalf("call %d with whitespace key %q should return true (no deduplication)", i, ws)
			}
		}
	}

	// 确保没有占用缓存 entries
	cache.mu.Lock()
	entryCount := len(cache.entries)
	cache.mu.Unlock()
	if entryCount != 0 {
		t.Fatalf("expected 0 entries in cache for whitespace keys, got %d", entryCount)
	}
}

func TestMemoryCache_TTLExpiration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 60*time.Second, 100)
	defer cache.Close()

	mockNow := time.Now()
	cache.nowFunc = func() time.Time {
		return mockNow
	}

	key := "expire-key"

	// 首次请求放行
	if !cache.CheckAndRecord(key) {
		t.Fatal("first call should succeed")
	}

	// 59秒后（未过期），仍然抑制
	mockNow = mockNow.Add(59 * time.Second)
	if cache.CheckAndRecord(key) {
		t.Fatal("call at 59s should be suppressed")
	}

	// 61秒后（已过期），重新放行
	mockNow = mockNow.Add(2 * time.Second) // total 61s
	if !cache.CheckAndRecord(key) {
		t.Fatal("call after TTL expiration (61s) should be allowed")
	}

	// 再次调用应该被抑制
	if cache.CheckAndRecord(key) {
		t.Fatal("immediate second call after refresh should be suppressed")
	}
}

func TestMemoryCache_MaxKeysAndCompaction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	maxKeys := 300
	cache := NewMemoryCache(ctx, 10*time.Second, maxKeys)
	defer cache.Close()

	mockNow := time.Now()
	cache.nowFunc = func() time.Time {
		return mockNow
	}

	// 1. 填满 maxKeys
	for i := 0; i < maxKeys; i++ {
		key := fmt.Sprintf("key-%d", i)
		if !cache.CheckAndRecord(key) {
			t.Fatalf("expected key %q to be recorded", key)
		}
	}

	cache.mu.Lock()
	countBefore := len(cache.entries)
	cache.mu.Unlock()
	if countBefore != maxKeys {
		t.Fatalf("expected %d entries, got %d", maxKeys, countBefore)
	}

	// 再插入一个 key，应当触顶快速淘汰一项，总大小依然为 maxKeys 防 OOM
	extraKey := "extra-key"
	if !cache.CheckAndRecord(extraKey) {
		t.Fatal("expected extra key to be recorded after evicting one entry")
	}

	cache.mu.Lock()
	countAfter := len(cache.entries)
	cache.mu.Unlock()
	if countAfter != maxKeys {
		t.Fatalf("expected %d entries after capacity eviction, got %d", maxKeys, countAfter)
	}

	// 2. 模拟大量过期并触发 Map 紧缩机制 (len > 256 && activeCount < len/2)
	// 让现有大部分 key 过期（前进 20s）
	mockNow = mockNow.Add(20 * time.Second)

	// 新插入少量活跃 key（例如 10 个）
	for i := 0; i < 10; i++ {
		activeKey := fmt.Sprintf("active-key-%d", i)
		if !cache.CheckAndRecord(activeKey) {
			t.Fatalf("failed to record active key: %s", activeKey)
		}
	}

	// 手动触发一次 evictAndCompactLocked（模拟后台 ticker 执行）
	cache.mu.Lock()
	entriesPtrBefore := fmt.Sprintf("%p", cache.entries)
	cache.evictAndCompactLocked(mockNow)
	entriesPtrAfter := fmt.Sprintf("%p", cache.entries)
	entriesCountAfterCompact := len(cache.entries)
	cache.mu.Unlock()

	// 验证已紧缩：存活项应当等于 10，并且 map 应该被重建（指针地址改变）
	if entriesCountAfterCompact != 10 {
		t.Fatalf("expected 10 active entries after compaction, got %d", entriesCountAfterCompact)
	}
	if entriesPtrBefore == entriesPtrAfter {
		t.Fatalf("expected map to be re-allocated for compaction, but pointer was unchanged")
	}
}

func TestMemoryCache_CapacityEviction_DoesNotEvictOnExpiredKeyUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const maxKeys = 20
	cache := NewMemoryCache(ctx, 100*time.Second, maxKeys)
	defer cache.Close()

	mockNow := time.Now()
	cache.nowFunc = func() time.Time {
		return mockNow
	}

	// 1. Record 10 expired keys at t=0
	for i := 0; i < 10; i++ {
		cache.CheckAndRecord(fmt.Sprintf("expired-key-%d", i))
	}

	// 2. Advance time to t=90s and record 10 active keys
	mockNow = mockNow.Add(90 * time.Second)
	for i := 0; i < 10; i++ {
		cache.CheckAndRecord(fmt.Sprintf("active-key-%d", i))
	}

	// 3. Advance time to t=105s (expired keys expired at 100s, active keys valid until 190s)
	mockNow = mockNow.Add(15 * time.Second)

	// 4. Refresh all 10 expired keys in-place
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("expired-key-%d", i)
		if !cache.CheckAndRecord(key) {
			t.Fatalf("expected %q to be allowed after expiration", key)
		}
	}

	// 5. All 10 active keys must still be present and suppressed within their TTL
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("active-key-%d", i)
		if cache.CheckAndRecord(key) {
			t.Fatalf("active key %q was prematurely evicted when updating expired keys", key)
		}
	}
}

func TestMemoryCache_ConcurrentRaceCondition(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 30*time.Second, 1000)
	defer cache.Close()

	key := "concurrent-target-key"
	const goroutines = 100

	var allowedCount int64
	var wg sync.WaitGroup
	startCh := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startCh
			if cache.CheckAndRecord(key) {
				atomic.AddInt64(&allowedCount, 1)
			}
		}()
	}

	// 所有 goroutine 同时起跑并发争抢同一个 key
	close(startCh)
	wg.Wait()

	if allowedCount != 1 {
		t.Fatalf("expected strictly 1 goroutine allowed, got %d", allowedCount)
	}
}

func TestMemoryCache_GracefulClose(t *testing.T) {
	ctx := context.Background()
	cache := NewMemoryCache(ctx, 10*time.Second, 100)

	// 多次调用 Close 幂等安全
	cache.Close()
	cache.Close()

	// Close 之后再次 CheckAndRecord 应当安全（不 panic）
	if !cache.CheckAndRecord("key-after-close") {
		t.Fatal("expected check after close to function safely")
	}
}

func TestMemoryCache_Defensive(t *testing.T) {
	// 1. nil 缓存指针防御
	var nilCache *MemoryCache
	if !nilCache.CheckAndRecord("any-key") {
		t.Fatal("nil cache CheckAndRecord should return true")
	}
	nilCache.Close() // 不 panic

	// 2. nil ctx 传入防御
	// nolint:staticcheck
	cacheWithNilCtx := NewMemoryCache(nil, 0, 0) // default ttl and maxKeys fallback
	if cacheWithNilCtx.ttl != 60*time.Second {
		t.Fatalf("expected default ttl 60s, got %v", cacheWithNilCtx.ttl)
	}
	if cacheWithNilCtx.maxKeys != 10000 {
		t.Fatalf("expected default maxKeys 10000, got %d", cacheWithNilCtx.maxKeys)
	}
	cacheWithNilCtx.Close()

	// 3. context cancel 退出测试
	ctx, cancel := context.WithCancel(context.Background())
	cacheWithCancel := NewMemoryCache(ctx, 10*time.Second, 100)
	cancel() // 取消 ctx
	// 等待协程退出
	cacheWithCancel.wg.Wait()
	cacheWithCancel.Close()
}

func TestMemoryCache_ZeroValueStruct(t *testing.T) {
	// 裸值与裸指针直接调用 CheckAndRecord 应当防御兜底初始化，不发生 panic 并能正常去重
	var zeroCache MemoryCache
	defer zeroCache.Close()

	if !zeroCache.CheckAndRecord("zero-key-1") {
		t.Fatal("first call on zero-value MemoryCache should return true (allowed)")
	}
	if zeroCache.CheckAndRecord("zero-key-1") {
		t.Fatal("second call on zero-value MemoryCache should return false (duplicate suppressed)")
	}

	ptrCache := &MemoryCache{}
	defer ptrCache.Close()

	if !ptrCache.CheckAndRecord("ptr-key-1") {
		t.Fatal("first call on &MemoryCache{} should return true (allowed)")
	}
	if ptrCache.CheckAndRecord("ptr-key-1") {
		t.Fatal("second call on &MemoryCache{} should return false (duplicate suppressed)")
	}
}

func TestMemoryCache_RoutineExpirationWithoutCompaction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cache := NewMemoryCache(ctx, 20*time.Second, 100)
	defer cache.Close()

	mockNow := time.Now()
	cache.nowFunc = func() time.Time {
		return mockNow
	}

	// 插入 5 个 key (len <= 256，不会触发紧缩，走常规删除)
	for i := 0; i < 5; i++ {
		cache.CheckAndRecord(fmt.Sprintf("item-%d", i))
	}

	// 前进 10 秒，再插入 3 个新 key
	mockNow = mockNow.Add(10 * time.Second)
	for i := 5; i < 8; i++ {
		cache.CheckAndRecord(fmt.Sprintf("item-%d", i))
	}

	// 前进 15 秒（前 5 个在 20s 过期，已过期；后 3 个在 30s 过期，仍存活）
	mockNow = mockNow.Add(15 * time.Second)

	cache.mu.Lock()
	ptrBefore := fmt.Sprintf("%p", cache.entries)
	cache.evictAndCompactLocked(mockNow)
	ptrAfter := fmt.Sprintf("%p", cache.entries)
	remainingCount := len(cache.entries)
	cache.mu.Unlock()

	// 存活应当为 3
	if remainingCount != 3 {
		t.Fatalf("expected 3 remaining entries, got %d", remainingCount)
	}
	// 不应发生 map 重建（指针保持不变）
	if ptrBefore != ptrAfter {
		t.Fatalf("expected map NOT to be re-allocated in routine eviction (len <= 256)")
	}

	// 验证后 3 个 key 依然在缓存中被抑制
	for i := 5; i < 8; i++ {
		key := fmt.Sprintf("item-%d", i)
		if cache.CheckAndRecord(key) {
			t.Fatalf("active key %q should still be suppressed", key)
		}
	}

	// 验证前 5 个 key 已经过期，重新允许
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("item-%d", i)
		if !cache.CheckAndRecord(key) {
			t.Fatalf("expired key %q should be allowed", key)
		}
	}
}

func TestMemoryCache_LongTTLIntervalCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// TTL 为 10 分钟，interval = ttl/2 = 5 分钟，触及上限 1 分钟封顶分支
	cache := NewMemoryCache(ctx, 10*time.Minute, 100)
	defer cache.Close()

	if !cache.CheckAndRecord("long-ttl-key") {
		t.Fatal("first call should succeed")
	}
}
