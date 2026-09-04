package cache

import (
	"testing"
	"time"
)

func TestManifestCache(t *testing.T) {
	c := NewManifestCache(50 * time.Millisecond)

	manifest := &ProductManifest{
		Product:   "geolite-city",
		Version:   "2026-09-04",
		SHA256:    "abcd1234",
		SizeBytes: 1000,
	}

	// 1. Get before set -> miss
	if _, ok := c.Get("geolite-city"); ok {
		t.Error("expected cache miss before Set")
	}

	// 2. Set and Get -> hit
	c.Set("geolite-city", manifest)
	cached, ok := c.Get("geolite-city")
	if !ok || cached.Version != "2026-09-04" {
		t.Errorf("expected cache hit, got ok=%v, cached=%+v", ok, cached)
	}

	// 3. Invalidate
	c.Invalidate("geolite-city")
	if _, ok := c.Get("geolite-city"); ok {
		t.Error("expected cache miss after Invalidate")
	}

	// 4. TTL expiration
	c.Set("geolite-city", manifest)
	time.Sleep(70 * time.Millisecond)
	if _, ok := c.Get("geolite-city"); ok {
		t.Error("expected cache miss after TTL expiration")
	}
}
