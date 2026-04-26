package primitives

import (
	"sync/atomic"

	"github.com/gogpu/gg/scene"
	"github.com/gogpu/ui/a11y"
	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	internalRender "github.com/gogpu/ui/internal/render"
	"github.com/gogpu/ui/widget"
)

// nextCacheKey is a monotonic counter for generating unique cache keys.
// Each RepaintBoundary gets a unique uint64 ID at creation time, used as
// the key into the dirty boundary tracking set. Atomic to be safe for
// concurrent RepaintBoundary creation across goroutines.
var nextCacheKey atomic.Uint64

// RepaintBoundary is a display widget that caches its child subtree as a
// scene.Scene display list. When the child subtree is clean (no dirty
// widgets), the cached display list is replayed into the parent canvas
// instead of re-executing Draw on every descendant.
//
// This is the Flutter RepaintBoundary / PictureLayer pattern: an explicit
// opt-in boundary that isolates expensive subtrees from the rest of the
// render tree. Display lists are portable across all gg backends (Vulkan,
// DX12, Metal, GLES, Software, future WASM/WebGPU).
//
// Cache lifecycle:
//   - The cached scene is allocated on first draw (lazy).
//   - The cache is invalidated when any descendant calls SetNeedsRedraw,
//     which propagates UP to the boundary (ADR-007 upward propagation),
//     or when the widget size changes.
//   - The cache is freed on Unmount.
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
	// into the Window's dirty boundary set. Assigned once at creation time.
	cacheKey uint64

	// cachedScene holds the recorded display list (scene.Scene) for the
	// child subtree. On cache hit, this is replayed into the parent canvas
	// via Canvas.ReplayScene — no child.Draw() re-execution needed.
	cachedScene *scene.Scene

	// cacheVersion is a monotonic counter incremented each time the cache
	// is refreshed. Used for observability and diagnostics.
	cacheVersion uint64
	// cacheWidth and cacheHeight track the cache dimensions to detect
	// size changes that require re-recording.
	cacheWidth  int
	cacheHeight int

	// debugLabel is an optional identifier for diagnostics.
	debugLabel string

	// cacheHits tracks how many times the cache was used (for stats).
	cacheHits int

	// boundaryDirty indicates that a descendant has changed and this
	// boundary's cache needs to be re-recorded. Set by upward dirty
	// propagation (ADR-007) via [MarkBoundaryDirty].
	boundaryDirty bool

	// onBoundaryDirty is a callback invoked when this boundary transitions
	// from clean to dirty. Used by the Window to collect dirty boundaries
	// into its dirtyBoundaries set (ADR-007, Task 1e).
	onBoundaryDirty func(rb *RepaintBoundary)
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
// of the given child widget as a scene.Scene display list.
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

// CacheValid reports whether the cached scene holds valid content.
func (rb *RepaintBoundary) CacheValid() bool {
	return !rb.boundaryDirty && rb.cachedScene != nil
}

// InvalidateCache marks the cache as stale, forcing a re-record on the
// next draw pass. This is called automatically when descendants are dirty;
// manual invocation is rarely needed.
func (rb *RepaintBoundary) InvalidateCache() {
	rb.boundaryDirty = true
}

// MarkBoundaryDirty marks this repaint boundary as needing re-rendering.
//
// Called by the upward dirty propagation in [widget.WidgetBase.SetNeedsRedraw]
// when a descendant widget changes. This invalidates the scene cache and
// notifies the Window (via callback) to add this boundary to its dirty set.
//
// Implements [widget.RepaintBoundaryMarker].
func (rb *RepaintBoundary) MarkBoundaryDirty() {
	if rb.boundaryDirty {
		return // Already dirty — O(1) guard.
	}
	rb.boundaryDirty = true

	if rb.onBoundaryDirty != nil {
		rb.onBoundaryDirty(rb)
	}
}

// IsBoundaryDirty reports whether this boundary has been marked dirty by
// upward propagation since the last draw pass.
func (rb *RepaintBoundary) IsBoundaryDirty() bool {
	return rb.boundaryDirty
}

// ClearBoundaryDirty resets the boundary dirty flag after the boundary
// has been repainted. Called by the Window after painting dirty boundaries.
func (rb *RepaintBoundary) ClearBoundaryDirty() {
	rb.boundaryDirty = false
}

// SetOnBoundaryDirty sets the callback invoked when this boundary
// transitions from clean to dirty via upward propagation.
//
// Used by the Window to collect dirty boundaries into its set.
func (rb *RepaintBoundary) SetOnBoundaryDirty(fn func(rb *RepaintBoundary)) {
	rb.onBoundaryDirty = fn
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
		rb.boundaryDirty = true
		rb.cacheWidth = w
		rb.cacheHeight = h
	}

	return size
}

// Draw renders the child subtree, using the scene cache when possible.
//
// On cache hit (boundary not dirty, cached scene exists): replays the
// cached scene.Scene into the canvas via Canvas.ReplayScene — no child
// re-execution. This is O(commands) via Encoding.Append or GPU dispatch.
//
// On cache miss (boundary dirty or first draw): records child.Draw into
// a new scene.Scene via SceneCanvas, then replays the result.
//
// This is the ADR-007 retained-mode pattern: display list per boundary.
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

	// Cache hit: boundary is clean and we have a cached scene.
	if !rb.boundaryDirty && rb.cachedScene != nil {
		rb.recordCacheHit(ctx)
		canvas.ReplayScene(rb.cachedScene)
		return
	}

	// Cache miss: record child drawing into a scene.
	if rb.cachedScene == nil {
		rb.cachedScene = scene.NewScene()
	}
	rb.cachedScene.Reset()

	recorder := internalRender.NewSceneCanvas(rb.cachedScene, w, h)
	rb.child.Draw(ctx, recorder)
	recorder.Close()

	// Clear redraw flags in the child subtree since we just rendered them.
	widget.ClearRedrawInTree(rb.child)

	rb.boundaryDirty = false
	rb.cacheVersion++

	// Replay the freshly recorded scene into the parent canvas.
	canvas.ReplayScene(rb.cachedScene)
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

// Unmount releases the scene cache when the widget is removed from the tree.
func (rb *RepaintBoundary) Unmount() {
	rb.cachedScene = nil
	rb.cacheVersion = 0
	rb.cacheWidth = 0
	rb.cacheHeight = 0
	rb.cacheHits = 0
	rb.boundaryDirty = false
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
	_ widget.Widget                = (*RepaintBoundary)(nil)
	_ widget.RepaintBoundaryMarker = (*RepaintBoundary)(nil)
	_ a11y.Accessible              = (*RepaintBoundary)(nil)
)
