package primitives

import (
	"image"
	"sync/atomic"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/scene"
	"github.com/gogpu/gpucontext"
	"github.com/gogpu/ui/a11y"
	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	internalRender "github.com/gogpu/ui/internal/render"
	"github.com/gogpu/ui/widget"
)

// nextCacheKey is a monotonic counter for generating unique cache keys.
// Each RepaintBoundary gets a unique uint64 ID at creation time, used as
// the key into the centralized ImageCache. Atomic to be safe for concurrent
// RepaintBoundary creation across goroutines.
var nextCacheKey atomic.Uint64

// sceneThresholdPixels is the minimum area (in pixels) for scene.Renderer
// activation. RepaintBoundaries with area below this threshold use the
// traditional gg.Context path (lower overhead for small widgets).
// RepaintBoundaries at or above this threshold use scene.Scene with
// tile-parallel rendering for better performance on large subtrees.
const sceneThresholdPixels = 128 * 128

// RepaintBoundary is a display widget that caches its child subtree as a
// CPU-side pixel buffer (image.RGBA). When the child subtree is clean (no
// dirty widgets), the cached image is composited directly onto the parent
// canvas instead of re-executing Draw on every descendant.
//
// This is the Flutter RepaintBoundary pattern: an explicit opt-in boundary
// that isolates expensive subtrees from the rest of the render tree.
// Users wrap widgets in RepaintBoundary at points where subtrees are
// expensive to draw and rarely change.
//
// For large widgets (>= 128x128 pixels), RepaintBoundary uses scene.Scene
// with tile-parallel rendering via scene.Renderer, providing better
// performance for complex subtrees. Small widgets use the traditional
// gg.Context path to avoid overhead.
//
// Cache lifecycle:
//   - The cache is allocated on first draw (lazy).
//   - The cache is invalidated when any descendant is dirty or the size changes.
//   - The cache is freed on Unmount or when the widget is garbage collected.
//
// RepaintBoundary implements [widget.Widget] and [a11y.Accessible].
//
// Example:
//
//	expensive := primitives.Box(
//	    primitives.Text("Complex chart..."),
//	).Padding(16)
//
//	cached := primitives.NewRepaintBoundary(expensive)
type RepaintBoundary struct {
	widget.WidgetBase

	child widget.Widget

	// cacheKey is a unique monotonic ID for this boundary, used as the key
	// into the centralized ImageCache. Assigned once at creation time.
	cacheKey uint64

	// cache holds the rendered child subtree as an RGBA pixmap.
	// Used as the local fallback when no centralized ImageCache is available.
	cache *image.RGBA
	// cacheValid indicates whether the cache is up to date.
	cacheValid bool
	// cacheVersion is a monotonic counter incremented each time the cache
	// is refreshed. Used as the version parameter for the centralized cache.
	cacheVersion uint64
	// cacheWidth and cacheHeight track the cache dimensions to detect
	// size changes that require reallocation.
	cacheWidth  int
	cacheHeight int

	// debugLabel is an optional identifier for diagnostics.
	debugLabel string

	// cacheHits tracks how many times the cache was used (for stats).
	cacheHits int

	// lastSharedCache holds a reference to the centralized ImageCache obtained
	// during the last Draw call. Used by Unmount to invalidate the cache entry
	// without needing a Context parameter.
	lastSharedCache widget.ImageCacheRef

	// GPU texture cache (Flutter OffsetLayer pattern).
	// When available, rendering goes directly to a GPU texture — zero CPU
	// readback. The texture is composited as a textured quad via DrawGPUTexture.
	gpuView    gpucontext.TextureView // cached GPU texture view
	gpuRelease func()                 // release function for the texture
	gpuWidth   int                    // cached texture width in pixels
	gpuHeight  int                    // cached texture height in pixels

	// gpuContext is a persistent gg.Context reused across frames for GPU
	// offscreen rendering. Enterprise pattern: Flutter and Chrome NEVER
	// create/destroy rendering contexts per layer per frame — both use
	// persistent contexts with texture pools. Created lazily on first GPU
	// render, released on Unmount or size change.
	gpuContext *gg.Context

	// scene.Scene integration (lazily initialized for large widgets).
	sceneRenderer *scene.Renderer
	sceneObj      *scene.Scene
	pixmap        *gg.Pixmap
}

// Option configures a [RepaintBoundary].
type Option func(*RepaintBoundary)

// WithDebugLabel sets an optional label for diagnostics and logging.
func WithDebugLabel(label string) Option {
	return func(rb *RepaintBoundary) {
		rb.debugLabel = label
	}
}

// NewRepaintBoundary creates a RepaintBoundary that caches the rendering
// of the given child widget.
//
// If child is nil, the boundary renders nothing and reports zero size.
//
// Options:
//   - [WithDebugLabel] — optional label for diagnostics
func NewRepaintBoundary(child widget.Widget, opts ...Option) *RepaintBoundary {
	rb := &RepaintBoundary{
		child:    child,
		cacheKey: nextCacheKey.Add(1),
	}
	rb.SetVisible(true)
	rb.SetEnabled(true)

	for _, opt := range opts {
		opt(rb)
	}

	return rb
}

// CacheKey returns the unique monotonic ID for this boundary.
// Used as the key into the centralized ImageCache.
func (rb *RepaintBoundary) CacheKey() uint64 {
	return rb.cacheKey
}

// Child returns the wrapped child widget.
func (rb *RepaintBoundary) Child() widget.Widget {
	return rb.child
}

// DebugLabel returns the diagnostic label, or empty string if none set.
func (rb *RepaintBoundary) DebugLabel() string {
	return rb.debugLabel
}

// CacheHits returns how many times the cache was served instead of re-rendering.
func (rb *RepaintBoundary) CacheHits() int {
	return rb.cacheHits
}

// CacheValid reports whether the cache currently holds valid content.
func (rb *RepaintBoundary) CacheValid() bool {
	return rb.cacheValid
}

// InvalidateCache marks the cache as stale, forcing a re-render on the
// next draw pass. This is called automatically when descendants are dirty;
// manual invocation is rarely needed.
func (rb *RepaintBoundary) InvalidateCache() {
	rb.cacheValid = false
}

// --- widget.Widget interface ---

// Layout delegates to the child and stores the resulting size.
func (rb *RepaintBoundary) Layout(ctx widget.Context, constraints geometry.Constraints) geometry.Size {
	if rb.child == nil {
		size := constraints.Constrain(geometry.Sz(0, 0))
		rb.SetBounds(geometry.FromPointSize(rb.Position(), size))
		return size
	}

	size := rb.child.Layout(ctx, constraints)

	// Position child at origin (no offset within boundary).
	rb.child.(interface{ SetBounds(geometry.Rect) }).SetBounds(
		geometry.FromPointSize(geometry.Pt(0, 0), size),
	)

	rb.SetBounds(geometry.FromPointSize(rb.Position(), size))

	// Invalidate cache if size changed.
	w := int(size.Width)
	h := int(size.Height)
	if w != rb.cacheWidth || h != rb.cacheHeight {
		rb.cacheValid = false
		rb.cacheWidth = w
		rb.cacheHeight = h
		// Release GPU context + texture — dimensions no longer match.
		// The persistent context will be recreated at the new size on
		// the next renderWithGPUTexture call.
		rb.releaseGPUResources()
	}

	return size
}

// Draw renders the child subtree, using the pixel cache when possible.
//
// If the child subtree is clean and the cache is valid, the cached image
// is composited directly. Otherwise, the child is rendered into an offscreen
// buffer, the result is captured as the new cache, and then composited.
//
// The method uses a two-level cache validation strategy (Phase 4, ADR-004):
//  1. Fast path: O(regions) spatial check via dirty.Tracker.Intersects
//  2. Slow path: O(tree_depth) recursive NeedsRedrawInTree walk
//
// When a centralized ImageCache is available (Phase 5), the cached image
// is stored there with LRU eviction. Otherwise, a per-widget local cache
// is used as a backward-compatible fallback.
func (rb *RepaintBoundary) Draw(ctx widget.Context, canvas widget.Canvas) {
	if !rb.IsVisible() || rb.child == nil {
		return
	}

	bounds := rb.Bounds()
	w := int(bounds.Width())
	h := int(bounds.Height())
	if w <= 0 || h <= 0 {
		return
	}

	// Fast path: spatial check via dirty.Tracker (Phase 4, ADR-004).
	// When the cache is valid and bounds don't overlap any dirty region,
	// the subtree is guaranteed clean — skip the O(tree_depth) walk.
	if rb.cacheValid && rb.tryFastPathCacheHit(ctx, canvas, bounds) {
		return
	}

	// Slow path: walk the subtree to determine dirty state.
	subtreeDirty := widget.NeedsRedrawInTree(rb.child)

	if rb.cacheValid && !subtreeDirty {
		// Cache hit via slow path — try GPU texture first, then CPU image.
		if rb.blitGPUTexture(canvas, bounds) {
			rb.recordCacheHit(ctx)
			return
		}
		img := rb.getCachedImage(ctx)
		if img != nil {
			rb.recordCacheHit(ctx)
			canvas.DrawImage(img, bounds.Min)
			return
		}
		// Centralized cache evicted our entry — fall through to re-render.
	}

	// Cache miss: render child into offscreen context.
	rb.renderToCache(ctx, w, h)

	// Clear redraw flags in the child subtree since we just rendered them.
	widget.ClearRedrawInTree(rb.child)

	// Blit the freshly rendered cache — GPU texture or CPU image.
	if rb.blitGPUTexture(canvas, bounds) {
		return
	}
	img := rb.getCachedImage(ctx)
	canvas.DrawImage(img, bounds.Min)
}

// tryFastPathCacheHit checks whether the boundary's bounds intersect any
// dirty region via the dirty.Tracker. Returns true if the fast path served
// the cached image (no intersection found), false if the caller should fall
// through to the slow path.
//
// This is O(regions) compared to NeedsRedrawInTree's O(tree_depth).
// For 100+ boundaries, this avoids 100 redundant tree walks.
func (rb *RepaintBoundary) tryFastPathCacheHit(
	ctx widget.Context, canvas widget.Canvas, bounds geometry.Rect,
) bool {
	tracker := dirtyTrackerFromContext(ctx)
	if tracker == nil {
		return false
	}

	if tracker.Intersects(bounds) {
		// Bounds overlap a dirty region — fall through to slow path.
		return false
	}

	// Bounds don't overlap any dirty region — guaranteed clean.
	// Try GPU texture first, then CPU image.
	if rb.blitGPUTexture(canvas, bounds) {
		rb.recordCacheHit(ctx)
		return true
	}
	img := rb.getCachedImage(ctx)
	if img == nil {
		// Centralized cache evicted our entry — fall through to re-render.
		return false
	}
	rb.recordCacheHit(ctx)
	canvas.DrawImage(img, bounds.Min)
	return true
}

// dirtyTrackerFromContext extracts the DirtyTrackerRef from the context
// via the DirtyTrackerProvider type assertion. Returns nil if the context
// does not provide a dirty tracker or if the tracker is nil.
func dirtyTrackerFromContext(ctx widget.Context) widget.DirtyTrackerRef {
	provider, ok := ctx.(widget.DirtyTrackerProvider)
	if !ok {
		return nil
	}
	return provider.DirtyTracker()
}

// recordCacheHit increments the cache hit counter and records the hit
// in the frame-level DrawStats for observability.
func (rb *RepaintBoundary) recordCacheHit(ctx widget.Context) {
	rb.cacheHits++

	provider, ok := ctx.(widget.DrawStatsProvider)
	if !ok {
		return
	}
	if stats := provider.DrawStats(); stats != nil {
		stats.CachedWidgets++
	}
}

// imageCacheFromContext extracts the ImageCacheRef from the context via
// the ImageCacheProvider type assertion. Returns nil if the context does
// not provide an image cache.
func imageCacheFromContext(ctx widget.Context) widget.ImageCacheRef {
	provider, ok := ctx.(widget.ImageCacheProvider)
	if !ok {
		return nil
	}
	return provider.ImageCache()
}

// getCachedImage retrieves the cached image from the centralized cache
// (if available) or from the local per-widget cache as fallback.
// Returns nil if no cached image exists (e.g., evicted from LRU).
func (rb *RepaintBoundary) getCachedImage(ctx widget.Context) *image.RGBA {
	if sharedCache := imageCacheFromContext(ctx); sharedCache != nil {
		img, ok := sharedCache.Get(rb.cacheKey)
		if ok {
			return img
		}
		// Entry was evicted from centralized cache.
		rb.cacheValid = false
		return nil
	}
	// Fallback: local cache.
	return rb.cache
}

// putCachedImage stores the cached image in the centralized cache (if
// available) and also in the local per-widget cache as fallback.
// The shared cache reference is saved for Unmount invalidation.
func (rb *RepaintBoundary) putCachedImage(ctx widget.Context, img *image.RGBA) {
	rb.cache = img
	rb.cacheValid = true
	rb.cacheVersion++

	if sharedCache := imageCacheFromContext(ctx); sharedCache != nil {
		rb.lastSharedCache = sharedCache
		sharedCache.Put(rb.cacheKey, img, rb.cacheVersion)
	}
}

// gpuTextureDrawer is an optional interface for canvases that support
// GPU texture compositing. The internal/render.Canvas implements this,
// allowing RepaintBoundary to blit cached GPU textures without modifying
// the public widget.Canvas interface.
type gpuTextureDrawer interface {
	DrawGPUTexture(view gpucontext.TextureView, x, y float64, width, height int)
}

// releaseGPUTexture frees the cached GPU texture resources (view + release
// function) but does NOT close the persistent gpuContext. Safe to call when
// no GPU texture is cached (no-op).
func (rb *RepaintBoundary) releaseGPUTexture() {
	if rb.gpuRelease != nil {
		rb.gpuRelease()
		rb.gpuRelease = nil
	}
	rb.gpuView = gpucontext.TextureView{}
}

// releaseGPUResources frees all GPU resources: the cached texture AND the
// persistent gg.Context. Called on Unmount and when dimensions change
// (requiring a new context + texture pair).
func (rb *RepaintBoundary) releaseGPUResources() {
	rb.releaseGPUTexture()
	if rb.gpuContext != nil {
		_ = rb.gpuContext.Close()
		rb.gpuContext = nil
	}
	rb.gpuWidth = 0
	rb.gpuHeight = 0
}

// renderWithGPUTexture attempts GPU-direct offscreen rendering using a
// persistent gg.Context (created once, reused across frames). Enterprise
// pattern: Flutter and Chrome NEVER create/destroy rendering contexts per
// layer per frame — both use persistent contexts with texture pools.
//
// The persistent context and texture are created on first call (or after a
// size change) and reused until Unmount or the next size change. On
// subsequent frames only BeginGPUFrame is called to reset LoadOp to Clear.
//
// Returns true if the GPU path succeeded, false if the caller should fall
// back to the CPU path (software backend or GPU unavailable).
func (rb *RepaintBoundary) renderWithGPUTexture(ctx widget.Context, w, h int) bool {
	// Create persistent context + texture only once (or when size changes).
	if rb.gpuContext == nil || rb.gpuWidth != w || rb.gpuHeight != h {
		rb.releaseGPUResources()

		dc := gg.NewContext(w, h)
		view, release := dc.CreateOffscreenTexture(w, h)
		if view.IsNil() {
			_ = dc.Close()
			return false
		}
		rb.gpuContext = dc
		rb.gpuView = view
		rb.gpuRelease = release
		rb.gpuWidth = w
		rb.gpuHeight = h
	}

	// Reset GPU frame state so the render pass uses LoadOpClear (not
	// LoadOpLoad which would preserve stale content from the previous frame).
	rb.gpuContext.BeginGPUFrame()

	// Draw child subtree into the persistent offscreen gg.Context.
	offscreen := internalRender.NewCanvas(rb.gpuContext, w, h)
	offscreen.Clear(widget.ColorTransparent)
	rb.child.Draw(ctx, offscreen)

	// Flush shapes + text to the persistent GPU texture — zero CPU readback.
	_ = rb.gpuContext.FlushGPUWithView(rb.gpuView, uint32(w), uint32(h)) //nolint:gosec // w,h are positive ints

	rb.cacheValid = true
	rb.cacheVersion++

	// GPU texture is the source of truth — clear the CPU image cache.
	rb.cache = nil

	return true
}

// blitGPUTexture composites the cached GPU texture onto the given canvas.
// Returns true if the GPU texture was blitted, false if the canvas does not
// support GPU texture drawing (caller should fall back to CPU DrawImage).
func (rb *RepaintBoundary) blitGPUTexture(canvas widget.Canvas, bounds geometry.Rect) bool {
	if rb.gpuView.IsNil() {
		return false
	}
	drawer, ok := canvas.(gpuTextureDrawer)
	if !ok {
		return false
	}
	drawer.DrawGPUTexture(rb.gpuView, float64(bounds.Min.X), float64(bounds.Min.Y),
		rb.gpuWidth, rb.gpuHeight)
	return true
}

// sceneMinDimension is the minimum width AND height (in pixels) required
// for scene.Renderer activation. Narrow strips (e.g., 792×28 list items)
// have enough area to exceed sceneThresholdPixels but only span one tile
// row, making tile-parallel rendering pure overhead. Both dimensions must
// be at least this value.
const sceneMinDimension = 64

// renderToCache selects the rendering strategy based on GPU availability
// and widget dimensions.
//
// Strategy priority (highest first):
//  1. GPU texture cache — direct GPU-to-GPU compositing (zero CPU readback).
//     This is the Flutter OffsetLayer pattern. Used when the GPU is available.
//  2. scene.Scene tile-parallel — CPU-side tile rendering for large widgets
//     (area >= sceneThresholdPixels, both dimensions >= sceneMinDimension).
//  3. gg.Context — traditional single-threaded CPU rendering for small widgets
//     or when the software backend is active.
func (rb *RepaintBoundary) renderToCache(ctx widget.Context, w, h int) {
	// Try GPU texture cache first (Flutter pattern, zero readback).
	if rb.renderWithGPUTexture(ctx, w, h) {
		return
	}

	// CPU fallback for software backend or when GPU is unavailable.
	if w*h >= sceneThresholdPixels && w >= sceneMinDimension && h >= sceneMinDimension {
		rb.renderWithScene(ctx, w, h)
	} else {
		rb.renderWithContext(ctx, w, h)
	}
}

// renderWithContext is the gg.Context-based rendering path for offscreen
// pixel caching. Used when the widget is too narrow for tile-parallel
// rendering or when the area is below sceneThresholdPixels.
//
// Offscreen rendering works correctly (per-context GPU isolation via
// ARCH-GG-001). The blocker is compositing: DrawImage on the main canvas
// triggers CPU Fill fallback (ImagePattern not GPU-accelerated), causing
// a mid-frame flush that breaks GPU-direct surface rendering.
// See TASK-GG-GPU-DRAWIMAGE-001.
func (rb *RepaintBoundary) renderWithContext(ctx widget.Context, w, h int) {
	dc := gg.NewContext(w, h)

	offscreen := internalRender.NewCanvas(dc, w, h)
	offscreen.Clear(widget.ColorTransparent)

	rb.child.Draw(ctx, offscreen)

	_ = dc.FlushGPU()

	img := dc.Image()
	rb.putCachedImage(ctx, internalRender.ToRGBA(img))
	_ = dc.Close()
}

// renderWithScene uses scene.Scene + scene.Renderer for tile-parallel rendering.
// The child subtree is drawn into a SceneCanvas (which records into scene.Scene),
// then the scene is rendered via tile-parallel scene.Renderer into a Pixmap.
func (rb *RepaintBoundary) renderWithScene(ctx widget.Context, w, h int) {
	// Initialize or resize scene.Renderer.
	if rb.sceneRenderer == nil || rb.sceneRenderer.Width() != w || rb.sceneRenderer.Height() != h {
		if rb.sceneRenderer != nil {
			rb.sceneRenderer.Close()
		}
		rb.sceneRenderer = scene.NewRenderer(w, h)
	}

	// Initialize or resize Pixmap.
	if rb.pixmap == nil || rb.pixmap.Width() != w || rb.pixmap.Height() != h {
		rb.pixmap = gg.NewPixmap(w, h)
	}

	// Initialize scene (reuse across frames).
	if rb.sceneObj == nil {
		rb.sceneObj = scene.NewScene()
	}

	// Reset scene for this frame.
	rb.sceneObj.Reset()

	// Build scene from child tree via SceneCanvas adapter.
	sceneCanvas := internalRender.NewSceneCanvas(rb.sceneObj, w, h)
	rb.child.Draw(ctx, sceneCanvas)
	sceneCanvas.Close()

	// Clear pixmap and render the scene.
	rb.pixmap.Clear(gg.Transparent)
	_ = rb.sceneRenderer.Render(rb.pixmap, rb.sceneObj)

	// Store in centralized cache (if available) and local cache.
	rb.putCachedImage(ctx, rb.pixmap.ToImage())
}

// Event dispatches events to the child.
func (rb *RepaintBoundary) Event(ctx widget.Context, e event.Event) bool {
	if !rb.IsVisible() || !rb.IsEnabled() {
		return false
	}

	if rb.child == nil {
		return false
	}

	// Translate mouse events to local coordinates.
	if me, ok := e.(*event.MouseEvent); ok {
		local := *me
		local.Position = me.Position.Sub(rb.Bounds().Min)
		return rb.child.Event(ctx, &local)
	}

	return rb.child.Event(ctx, e)
}

// Children returns the child widget, or nil if none.
func (rb *RepaintBoundary) Children() []widget.Widget {
	if rb.child == nil {
		return nil
	}
	return []widget.Widget{rb.child}
}

// Unmount releases the pixel cache and scene resources when the widget is
// removed from the tree. If a centralized ImageCache was used, the entry
// is invalidated to free memory immediately rather than waiting for LRU
// eviction.
func (rb *RepaintBoundary) Unmount() {
	// Release all GPU resources (persistent context + texture).
	rb.releaseGPUResources()

	// Invalidate from centralized cache if one was used.
	if rb.lastSharedCache != nil {
		rb.lastSharedCache.Invalidate(rb.cacheKey)
		rb.lastSharedCache = nil
	}

	rb.cache = nil
	rb.cacheValid = false
	rb.cacheVersion = 0
	rb.cacheWidth = 0
	rb.cacheHeight = 0
	rb.cacheHits = 0

	// Release scene resources.
	if rb.sceneRenderer != nil {
		rb.sceneRenderer.Close()
		rb.sceneRenderer = nil
	}
	rb.sceneObj = nil
	rb.pixmap = nil
}

// --- a11y.Accessible interface ---

// AccessibilityRole returns [a11y.RoleGenericContainer].
func (rb *RepaintBoundary) AccessibilityRole() a11y.Role {
	return a11y.RoleGenericContainer
}

// AccessibilityLabel returns the debug label or empty string.
func (rb *RepaintBoundary) AccessibilityLabel() string {
	return rb.debugLabel
}

// AccessibilityHint returns an empty string.
func (rb *RepaintBoundary) AccessibilityHint() string {
	return ""
}

// AccessibilityValue returns an empty string.
func (rb *RepaintBoundary) AccessibilityValue() string {
	return ""
}

// AccessibilityState returns the default state.
func (rb *RepaintBoundary) AccessibilityState() a11y.State {
	return a11y.State{
		Disabled: !rb.IsEnabled(),
		Hidden:   !rb.IsVisible(),
	}
}

// AccessibilityActions returns nil.
func (rb *RepaintBoundary) AccessibilityActions() []a11y.Action {
	return nil
}

// Compile-time interface checks.
var (
	_ widget.Widget   = (*RepaintBoundary)(nil)
	_ a11y.Accessible = (*RepaintBoundary)(nil)
)
