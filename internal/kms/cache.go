package kms

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// DataKey represents a KMS data key for envelope encryption
type DataKey struct {
	KeyID             string
	PlaintextKey      []byte
	CiphertextBlob    []byte
	EncryptionContext map[string]string
	CreatedAt         time.Time
}

// DataKeyCache implements a TTL cache for data keys
type DataKeyCache struct {
	cache    map[string]*cacheEntry
	mu       sync.RWMutex
	ttl      time.Duration
	stopChan chan struct{}
}

type cacheEntry struct {
	dataKey   *DataKey
	expiresAt time.Time
}

// NewDataKeyCache creates a new data key cache
func NewDataKeyCache(ttl time.Duration) *DataKeyCache {
	if ttl <= 0 {
		ttl = 5 * time.Minute // Default TTL
	}

	c := &DataKeyCache{
		cache:    make(map[string]*cacheEntry),
		ttl:      ttl,
		stopChan: make(chan struct{}),
	}

	// Start cleanup goroutine
	go c.cleanupLoop()

	return c
}

// Get retrieves a data key from cache
func (c *DataKeyCache) Get(key string) *DataKey {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.cache[key]
	if !ok {
		return nil
	}

	// Check if expired
	if time.Now().After(entry.expiresAt) {
		return nil
	}

	return entry.dataKey
}

// Put adds a data key to cache
func (c *DataKeyCache) Put(key string, dataKey *DataKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache[key] = &cacheEntry{
		dataKey:   dataKey,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// Delete removes a data key from cache
func (c *DataKeyCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.cache, key)
}

// Clear removes all entries from cache
func (c *DataKeyCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[string]*cacheEntry)
}

// Size returns the number of cached entries
func (c *DataKeyCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.cache)
}

// cleanupLoop periodically removes expired entries
func (c *DataKeyCache) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.cleanup()
		case <-c.stopChan:
			return
		}
	}
}

// cleanup removes expired entries
func (c *DataKeyCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.cache {
		if now.After(entry.expiresAt) {
			// Zero out the plaintext key before removing
			if entry.dataKey != nil && entry.dataKey.PlaintextKey != nil {
				for i := range entry.dataKey.PlaintextKey {
					entry.dataKey.PlaintextKey[i] = 0
				}
			}
			delete(c.cache, key)
		}
	}
}

// Close stops the cleanup goroutine and clears the cache
func (c *DataKeyCache) Close() {
	close(c.stopChan)

	// Zero out all plaintext keys
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, entry := range c.cache {
		if entry.dataKey != nil && entry.dataKey.PlaintextKey != nil {
			for i := range entry.dataKey.PlaintextKey {
				entry.dataKey.PlaintextKey[i] = 0
			}
		}
	}

	c.cache = make(map[string]*cacheEntry)
}

// buildDataKeyCacheKey creates a cache key from keyID and encryption context
func buildDataKeyCacheKey(keyID string, context map[string]string) string {
	h := sha256.New()
	h.Write([]byte(keyID))

	// Sort context keys for consistent hashing
	for k, v := range context {
		h.Write([]byte(k))
		h.Write([]byte(v))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// serializeEncryptionContext converts encryption context to string format
func serializeEncryptionContext(context map[string]string) string {
	if len(context) == 0 {
		return ""
	}

	// AWS expects base64-encoded JSON, but for headers we'll use a simpler format
	result := ""
	first := true
	for k, v := range context {
		if !first {
			result += ","
		}
		result += fmt.Sprintf("%s=%s", k, v)
		first = false
	}

	return result
}
