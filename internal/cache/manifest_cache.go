package cache

import (
	"sync"
	"time"
)

type ProductManifest struct {
	Product     string    `json:"product"`
	EditionID   string    `json:"edition_id"`
	Version     string    `json:"version"`
	ReleasedAt  time.Time `json:"released_at"`
	SHA256      string    `json:"sha256"`
	SizeBytes   int64     `json:"size_bytes"`
	Filename    string    `json:"filename"`
	DownloadURL string    `json:"download_url"`
}

type cacheEntry struct {
	manifest *ProductManifest
	cachedAt time.Time
}

// ManifestCache provides an in-memory thread-safe cache for product manifests.
type ManifestCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
}

func NewManifestCache(ttl time.Duration) *ManifestCache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &ManifestCache{
		entries: make(map[string]*cacheEntry),
		ttl:     ttl,
	}
}

func (c *ManifestCache) Get(productID string) (*ProductManifest, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[productID]
	if !ok {
		return nil, false
	}
	if time.Since(entry.cachedAt) > c.ttl {
		return nil, false
	}
	return entry.manifest, true
}

func (c *ManifestCache) Set(productID string, manifest *ProductManifest) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[productID] = &cacheEntry{
		manifest: manifest,
		cachedAt: time.Now(),
	}
}

func (c *ManifestCache) Invalidate(productID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, productID)
}

func (c *ManifestCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*cacheEntry)
}
