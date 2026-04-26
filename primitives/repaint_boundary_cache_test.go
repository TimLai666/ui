package primitives_test

import (
	"testing"

	"github.com/gogpu/ui/geometry"
	internalRender "github.com/gogpu/ui/internal/render"
	"github.com/gogpu/ui/primitives"
	"github.com/gogpu/ui/widget"
)

// --- Phase 5: Centralized ImageCache Integration Tests ---

// newContextWithImageCache creates a ContextImpl with a centralized ImageCache
// configured. This simulates the Window.newWindow() setup where the cache is
// set on the context.
func newContextWithImageCache(maxSizeMB int) (*widget.ContextImpl, *internalRender.ImageCache) {
	ctx := widget.NewContext()
	cache := internalRender.NewImageCache(maxSizeMB)
	ctx.SetImageCache(cache)
	return ctx, cache
}

func TestRepaintBoundary_CentralizedCache_UsedWhenAvailable(t *testing.T) {
	// When a centralized ImageCache is available on the context,
	// RepaintBoundary should store its rendered image there.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx, cache := newContextWithImageCache(64)
	canvas := &imageRecordingCanvas{}

	// First draw: cache miss → renders child, puts image in cache.
	rb.Draw(ctx, canvas)

	if cache.EntryCount() != 1 {
		t.Errorf("cache.EntryCount = %d, want 1", cache.EntryCount())
	}
	if !cache.Contains(rb.CacheKey()) {
		t.Error("cache should contain entry for this boundary's key")
	}
}

func TestRepaintBoundary_CentralizedCache_CacheHitFromSharedCache(t *testing.T) {
	// On second draw (clean subtree), RepaintBoundary should retrieve
	// the image from the centralized cache.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx, cache := newContextWithImageCache(64)

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas) // First draw: cache miss, renders child, puts + gets.

	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2) // Second draw: cache hit.

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1 (second draw should use cache)", child.drawCount)
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}

	// The shared cache sees 2 hits: one from the first draw's blit
	// (after renderToCache puts the entry, getCachedImage gets it),
	// and one from the second draw's slow path cache hit.
	stats := cache.Stats()
	if stats.Hits < 1 {
		t.Errorf("cache.Stats().Hits = %d, want >= 1", stats.Hits)
	}
}

func TestRepaintBoundary_CentralizedCache_FallbackToLocalWhenNoCache(t *testing.T) {
	// When no centralized cache is available (nil ctx or context without
	// ImageCacheProvider), RepaintBoundary should fall back to local cache.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// Draw with nil context.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas) // First draw.
	rb.Draw(nil, canvas) // Second draw: local cache hit.

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1 (local fallback cache hit)", child.drawCount)
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}

func TestRepaintBoundary_CentralizedCache_FallbackWhenNoProvider(t *testing.T) {
	// When context implements Context but NOT ImageCacheProvider,
	// RepaintBoundary should fall back to local cache.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx := widget.NewContext()
	// Do NOT set image cache — it stays nil.

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas) // First draw.
	rb.Draw(ctx, canvas) // Second draw: local cache hit.

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1", child.drawCount)
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}

func TestRepaintBoundary_CentralizedCache_UnmountInvalidatesEntry(t *testing.T) {
	// On Unmount, RepaintBoundary should invalidate its entry from the
	// centralized cache so memory is freed immediately.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx, cache := newContextWithImageCache(64)

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas) // Populates centralized cache.

	if !cache.Contains(rb.CacheKey()) {
		t.Fatal("pre-condition: cache should contain entry")
	}

	rb.Unmount()

	if cache.Contains(rb.CacheKey()) {
		t.Error("cache entry should be removed after Unmount")
	}
	if rb.CacheValid() {
		t.Error("local cache should be invalid after Unmount")
	}
}

func TestRepaintBoundary_CentralizedCache_LRUEvictionHandled(t *testing.T) {
	// When the centralized cache evicts an entry (LRU), RepaintBoundary
	// should gracefully handle the miss and re-render without panic.
	// This test only verifies eviction happens and re-render is correct —
	// it does not assert cache hit ratio because LRU thrashing is expected
	// when working set exceeds cache size.

	// Create a tiny cache: ~1MB. Each 100x50 image = 20000 bytes.
	// Cache can hold 1MB / 20000 = ~52 entries. Use more boundaries
	// than the cache can hold.
	ctx, cache := newContextWithImageCache(1)

	const numBoundaries = 80 // More than ~52 max entries.
	boundaries := make([]*primitives.RepaintBoundary, numBoundaries)
	children := make([]*drawCountingWidget, numBoundaries)

	for i := range numBoundaries {
		children[i] = newDrawCountingWidget()
		boundaries[i] = primitives.NewRepaintBoundary(children[i])
		boundaries[i].Layout(nil, geometry.BoxConstraints(0, 100, 0, 50))
	}

	// First draw: all boundaries render and populate cache.
	for i := range numBoundaries {
		canvas := &imageRecordingCanvas{}
		boundaries[i].Draw(ctx, canvas)
	}

	// Some entries should have been evicted (more boundaries than capacity).
	stats := cache.Stats()
	if stats.Evictions == 0 {
		t.Error("expected LRU evictions with more boundaries than cache can hold")
	}

	// Draw all boundaries again. The key assertion: no panic, no stale data,
	// every boundary produces a DrawImage call with valid content.
	for i := range numBoundaries {
		canvas := &imageRecordingCanvas{}
		boundaries[i].Draw(ctx, canvas)

		// Should always produce a DrawImage call regardless of hit/miss.
		if len(canvas.drawImageCalls) != 1 {
			t.Errorf("boundary[%d]: expected 1 DrawImage call, got %d",
				i, len(canvas.drawImageCalls))
		}
		// Image must never be nil.
		if canvas.drawImageCalls[0].img == nil {
			t.Errorf("boundary[%d]: DrawImage received nil image", i)
		}
	}
}

func TestRepaintBoundary_CentralizedCache_MultipleBoundariesShareCache(t *testing.T) {
	// Multiple RepaintBoundary instances should share the same cache.
	ctx, cache := newContextWithImageCache(64)

	rb1 := primitives.NewRepaintBoundary(newDrawCountingWidget())
	rb2 := primitives.NewRepaintBoundary(newDrawCountingWidget())
	rb3 := primitives.NewRepaintBoundary(newDrawCountingWidget())

	for _, rb := range []*primitives.RepaintBoundary{rb1, rb2, rb3} {
		rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))
	}

	// Draw all three.
	for _, rb := range []*primitives.RepaintBoundary{rb1, rb2, rb3} {
		canvas := &imageRecordingCanvas{}
		rb.Draw(ctx, canvas)
	}

	if cache.EntryCount() != 3 {
		t.Errorf("cache.EntryCount = %d, want 3", cache.EntryCount())
	}

	// Each boundary should have a unique key.
	if rb1.CacheKey() == rb2.CacheKey() || rb2.CacheKey() == rb3.CacheKey() {
		t.Error("boundaries should have unique cache keys")
	}
}

func TestRepaintBoundary_CacheKey_UniquePerBoundary(t *testing.T) {
	// Every RepaintBoundary should get a unique, monotonically increasing key.
	keys := make(map[uint64]bool)
	for range 100 {
		rb := primitives.NewRepaintBoundary(nil)
		if keys[rb.CacheKey()] {
			t.Fatalf("duplicate cache key: %d", rb.CacheKey())
		}
		keys[rb.CacheKey()] = true
	}
}

func TestRepaintBoundary_CentralizedCache_InvalidateAllOnSetRoot(t *testing.T) {
	// Simulate the Window.SetRoot() behavior: InvalidateAll should clear
	// all entries when the root widget changes.
	ctx, cache := newContextWithImageCache(64)

	rb := primitives.NewRepaintBoundary(newDrawCountingWidget())
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas)

	if cache.EntryCount() != 1 {
		t.Fatalf("pre-condition: cache.EntryCount = %d, want 1", cache.EntryCount())
	}

	// Simulate SetRoot: invalidate all.
	cache.InvalidateAll()

	if cache.EntryCount() != 0 {
		t.Errorf("cache.EntryCount = %d after InvalidateAll, want 0", cache.EntryCount())
	}
}

func TestRepaintBoundary_CentralizedCache_DirtySubtreeReRendersAndUpdatesCache(t *testing.T) {
	// When the subtree is dirty, RepaintBoundary should re-render and
	// update the centralized cache with fresh content.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx, cache := newContextWithImageCache(64)

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas) // First draw.

	if child.drawCount != 1 {
		t.Fatalf("pre-condition: child drawn %d times, want 1", child.drawCount)
	}

	// Mark child dirty.
	child.SetNeedsRedraw(true)

	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2) // Second draw: dirty → re-render.

	if child.drawCount != 2 {
		t.Errorf("child drawn %d times, want 2 (dirty child)", child.drawCount)
	}

	// Cache should still have exactly 1 entry (updated, not duplicated).
	if cache.EntryCount() != 1 {
		t.Errorf("cache.EntryCount = %d, want 1 (updated in place)", cache.EntryCount())
	}
}
