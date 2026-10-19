// Package idempotency provides in-memory deduplication caching with periodic compaction.
package idempotency

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Checker determines whether a key has been processed recently.
type Checker interface {
	CheckAndRecord(key string) bool
}

// MemoryCache implements an in-memory TTL deduplication cache with periodic compaction.
type MemoryCache struct {
	mu        sync.Mutex
	entries   map[string]time.Time
	ttl       time.Duration
	maxKeys   int
	stopCh    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	nowFunc   func() time.Time // 可注入以进行确定性单元测试
}

// NewMemoryCache creates and starts an in-memory deduplication cache.
func NewMemoryCache(ctx context.Context, ttl time.Duration, maxKeys int) *MemoryCache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	if maxKeys <= 0 {
		maxKeys = 10000
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c := &MemoryCache{
		entries: make(map[string]time.Time),
		ttl:     ttl,
		maxKeys: maxKeys,
		stopCh:  make(chan struct{}),
		nowFunc: time.Now,
	}

	c.wg.Add(1)
	go c.runEvictionLoop(ctx)
	return c
}

// CheckAndRecord atomically checks if key is duplicate. Empty or whitespace-only key returns true (no deduplication).
func (c *MemoryCache) CheckAndRecord(key string) bool {
	if c == nil {
		return true
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[string]time.Time)
	}
	if c.nowFunc == nil {
		c.nowFunc = time.Now
	}
	if c.ttl <= 0 {
		c.ttl = 60 * time.Second
	}
	if c.maxKeys <= 0 {
		c.maxKeys = 10000
	}

	now := c.nowFunc()
	expireAt, exists := c.entries[key]
	if exists {
		if now.Before(expireAt) {
			return false // 重复命中且未过期
		}
		// 已过期，允许覆盖占位，不增加 map 长度
	}

	// 容量上限兜底：达到 maxKeys 且即将插入新 key 时快速淘汰一项，保证常数级锁持有时间
	if !exists && len(c.entries) >= c.maxKeys {
		for k := range c.entries {
			delete(c.entries, k)
			break
		}
	}

	c.entries[key] = now.Add(c.ttl)
	return true
}

// Close gracefully stops the background eviction worker.
func (c *MemoryCache) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		if c.stopCh != nil {
			close(c.stopCh)
		}
	})
	c.wg.Wait()
}

func (c *MemoryCache) runEvictionLoop(ctx context.Context) {
	defer c.wg.Done()
	interval := c.ttl / 2
	if interval < 10*time.Second {
		interval = 10 * time.Second
	} else if interval > 1*time.Minute {
		interval = 1 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}

	for {
		select {
		case <-c.stopCh:
			return
		case <-ctxDone:
			return
		case <-ticker.C:
			c.mu.Lock()
			nowFn := c.nowFunc
			if nowFn == nil {
				nowFn = time.Now
			}
			c.evictAndCompactLocked(nowFn())
			c.mu.Unlock()
		}
	}
}

func (c *MemoryCache) evictAndCompactLocked(now time.Time) {
	activeCount := 0
	for _, exp := range c.entries {
		if now.Before(exp) {
			activeCount++
		}
	}

	// 紧缩机制：当 map 曾扩容（>256）且存活率低于 50% 时重建 map，回收底层哈希桶内存
	if len(c.entries) > 256 && activeCount < len(c.entries)/2 {
		compacted := make(map[string]time.Time, activeCount)
		for k, exp := range c.entries {
			if now.Before(exp) {
				compacted[k] = exp
			}
		}
		c.entries = compacted
		return
	}

	// 常规删除已过期项
	for k, exp := range c.entries {
		if !now.Before(exp) {
			delete(c.entries, k)
		}
	}
}
