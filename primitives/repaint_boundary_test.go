package primitives_test

import (
	"image"
	"testing"

	"github.com/gogpu/ui/a11y"
	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/primitives"
	"github.com/gogpu/ui/widget"
)

// drawCountingWidget tracks how many times Draw is called.
type drawCountingWidget struct {
	widget.WidgetBase
	drawCount int
}

func newDrawCountingWidget() *drawCountingWidget {
	w := &drawCountingWidget{}
	w.SetVisible(true)
	w.SetEnabled(true)
	w.SetNeedsRedraw(true)
	return w
}

func (w *drawCountingWidget) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(100, 50))
}

func (w *drawCountingWidget) Draw(_ widget.Context, _ widget.Canvas) {
	w.drawCount++
}

func (w *drawCountingWidget) Event(_ widget.Context, _ event.Event) bool { return false }

var _ widget.Widget = (*drawCountingWidget)(nil)

// imageRecordingCanvas records DrawImage calls for validation.
type imageRecordingCanvas struct {
	mockCanvas
	drawImageCalls []drawImageCall
}

type drawImageCall struct {
	img image.Image
	at  geometry.Point
}

func (c *imageRecordingCanvas) DrawImage(img image.Image, at geometry.Point) {
	c.drawImageCalls = append(c.drawImageCalls, drawImageCall{img: img, at: at})
}

// --- Construction Tests ---

func TestNewRepaintBoundary_NilChild(t *testing.T) {
	rb := primitives.NewRepaintBoundary(nil)
	if rb == nil {
		t.Fatal("NewRepaintBoundary should never return nil")
	}
	if rb.Child() != nil {
		t.Error("expected nil child")
	}
	if rb.Children() != nil {
		t.Error("expected nil children slice for nil child")
	}
}

func TestNewRepaintBoundary_WithChild(t *testing.T) {
	child := primitives.Text("hello")
	rb := primitives.NewRepaintBoundary(child)

	if rb.Child() != child {
		t.Error("expected child to be returned")
	}
	children := rb.Children()
	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}
	if children[0] != child {
		t.Error("expected child in Children() slice")
	}
}

func TestNewRepaintBoundary_DefaultState(t *testing.T) {
	rb := primitives.NewRepaintBoundary(primitives.Text("x"))

	if !rb.IsVisible() {
		t.Error("should be visible by default")
	}
	if !rb.IsEnabled() {
		t.Error("should be enabled by default")
	}
	if rb.CacheValid() {
		t.Error("cache should not be valid initially")
	}
	if rb.CacheHits() != 0 {
		t.Error("cache hits should be 0 initially")
	}
	if rb.DebugLabel() != "" {
		t.Error("debug label should be empty by default")
	}
}

func TestNewRepaintBoundary_WithDebugLabel(t *testing.T) {
	rb := primitives.NewRepaintBoundary(nil, primitives.WithDebugLabel("chart"))
	if rb.DebugLabel() != "chart" {
		t.Errorf("expected debug label 'chart', got %q", rb.DebugLabel())
	}
}

// --- Layout Tests ---

func TestRepaintBoundary_Layout_NilChild(t *testing.T) {
	rb := primitives.NewRepaintBoundary(nil)
	constraints := geometry.Tight(geometry.Sz(200, 100))
	size := rb.Layout(nil, constraints)

	if size.Width != 200 || size.Height != 100 {
		t.Errorf("expected tight size 200x100, got %v", size)
	}
}

func TestRepaintBoundary_Layout_DelegatesToChild(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	constraints := geometry.BoxConstraints(0, 200, 0, 100)
	size := rb.Layout(nil, constraints)

	// drawCountingWidget returns Constrain(100, 50)
	if size.Width != 100 || size.Height != 50 {
		t.Errorf("expected 100x50, got %v", size)
	}
}

func TestRepaintBoundary_Layout_InvalidatesCacheOnSizeChange(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	// First layout
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// Force a draw to populate cache
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if !rb.CacheValid() {
		t.Error("cache should be valid after first draw")
	}

	// Change constraints to produce different size
	rb.Layout(nil, geometry.Tight(geometry.Sz(50, 25)))

	if rb.CacheValid() {
		t.Error("cache should be invalidated after size change")
	}
}

// --- Draw Tests ---

func TestRepaintBoundary_Draw_Invisible(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.SetVisible(false)

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if child.drawCount > 0 {
		t.Error("invisible boundary should not draw child")
	}
	if len(canvas.drawImageCalls) > 0 {
		t.Error("invisible boundary should not call DrawImage")
	}
}

func TestRepaintBoundary_Draw_NilChild(t *testing.T) {
	rb := primitives.NewRepaintBoundary(nil)
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if len(canvas.drawImageCalls) > 0 {
		t.Error("nil child should not call DrawImage")
	}
}

func TestRepaintBoundary_Draw_FirstDrawRendersChild(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if child.drawCount != 1 {
		t.Errorf("expected child Draw called once, got %d", child.drawCount)
	}
	if len(canvas.drawImageCalls) != 1 {
		t.Fatalf("expected 1 DrawImage call, got %d", len(canvas.drawImageCalls))
	}
	if rb.CacheHits() != 0 {
		t.Error("first draw should not be a cache hit")
	}
	if !rb.CacheValid() {
		t.Error("cache should be valid after draw")
	}
}

func TestRepaintBoundary_Draw_SecondDrawUsesCacheWhenClean(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	// First draw: renders child, populates cache.
	rb.Draw(nil, canvas)

	// Child is now clean (ClearRedrawInTree called by RepaintBoundary.Draw).
	// Second draw should use cache.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(nil, canvas2)

	if child.drawCount != 1 {
		t.Errorf("expected child Draw called once total, got %d", child.drawCount)
	}
	if len(canvas2.drawImageCalls) != 1 {
		t.Fatalf("expected 1 DrawImage call on second draw, got %d", len(canvas2.drawImageCalls))
	}
	if rb.CacheHits() != 1 {
		t.Errorf("expected 1 cache hit, got %d", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_DirtyChildInvalidatesCache(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas) // First draw

	// Mark child dirty.
	child.SetNeedsRedraw(true)

	canvas2 := &imageRecordingCanvas{}
	rb.Draw(nil, canvas2) // Second draw: child dirty, must re-render.

	if child.drawCount != 2 {
		t.Errorf("expected child Draw called twice, got %d", child.drawCount)
	}
	if rb.CacheHits() != 0 {
		t.Errorf("expected 0 cache hits (dirty child), got %d", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_ManualInvalidation(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas) // First draw

	// Manually invalidate cache.
	rb.InvalidateCache()

	if rb.CacheValid() {
		t.Error("cache should be invalid after InvalidateCache")
	}

	canvas2 := &imageRecordingCanvas{}
	rb.Draw(nil, canvas2)

	if child.drawCount != 2 {
		t.Errorf("expected child Draw called twice after manual invalidation, got %d", child.drawCount)
	}
}

func TestRepaintBoundary_Draw_ZeroSizeSkipsRendering(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	// Set zero-size bounds.
	rb.SetBounds(geometry.NewRect(0, 0, 0, 0))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if child.drawCount > 0 {
		t.Error("zero-size boundary should not draw child")
	}
	if len(canvas.drawImageCalls) > 0 {
		t.Error("zero-size boundary should not call DrawImage")
	}
}

func TestRepaintBoundary_Draw_PositionPassedToDrawImage(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))
	rb.SetBounds(geometry.NewRect(50, 30, 100, 50))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if len(canvas.drawImageCalls) != 1 {
		t.Fatalf("expected 1 DrawImage call, got %d", len(canvas.drawImageCalls))
	}

	at := canvas.drawImageCalls[0].at
	if at.X != 50 || at.Y != 30 {
		t.Errorf("expected DrawImage at (50,30), got (%v,%v)", at.X, at.Y)
	}
}

// --- Nested RepaintBoundary Tests ---

func TestRepaintBoundary_Nested(t *testing.T) {
	innerChild := newDrawCountingWidget()
	inner := primitives.NewRepaintBoundary(innerChild)

	outer := primitives.NewRepaintBoundary(inner)
	outer.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	outer.Draw(nil, canvas) // First draw: both render.

	if innerChild.drawCount != 1 {
		t.Errorf("expected inner child Draw once, got %d", innerChild.drawCount)
	}

	// Second draw: outer serves from cache (inner is also clean).
	canvas2 := &imageRecordingCanvas{}
	outer.Draw(nil, canvas2)

	if innerChild.drawCount != 1 {
		t.Errorf("expected inner child Draw still 1 (outer cached), got %d", innerChild.drawCount)
	}
	if outer.CacheHits() != 1 {
		t.Errorf("expected outer cache hit, got %d", outer.CacheHits())
	}
}

// --- Event Tests ---

func TestRepaintBoundary_Event_DelegatesToChild(t *testing.T) {
	consumed := false
	child := &eventTestWidget{
		onEvent: func() { consumed = true },
	}
	child.SetVisible(true)
	child.SetEnabled(true)
	child.SetBounds(geometry.NewRect(0, 0, 100, 50))

	rb := primitives.NewRepaintBoundary(child)
	rb.SetBounds(geometry.NewRect(10, 10, 100, 50))

	// Send key event (non-mouse, no coordinate translation needed).
	ke := event.NewKeyEvent(event.KeyPress, event.KeyA, 'a', 0)
	result := rb.Event(nil, ke)

	if !consumed {
		t.Error("event should be dispatched to child")
	}
	if !result {
		t.Error("event should be consumed")
	}
}

func TestRepaintBoundary_Event_TranslatesMouseCoordinates(t *testing.T) {
	var receivedPos geometry.Point
	child := &mouseTrackingWidget{
		onMouse: func(pos geometry.Point) { receivedPos = pos },
	}
	child.SetVisible(true)
	child.SetEnabled(true)
	child.SetBounds(geometry.NewRect(0, 0, 100, 50))

	rb := primitives.NewRepaintBoundary(child)
	rb.SetBounds(geometry.NewRect(20, 30, 100, 50))

	pos := geometry.Pt(50, 40)
	me := event.NewMouseEvent(event.MousePress, event.ButtonLeft, 0, pos, pos, 0)
	rb.Event(nil, me)

	// Mouse position should be translated: (50-20, 40-30) = (30, 10)
	if receivedPos.X != 30 || receivedPos.Y != 10 {
		t.Errorf("expected translated position (30,10), got (%v,%v)", receivedPos.X, receivedPos.Y)
	}
}

func TestRepaintBoundary_Event_InvisibleIgnoresEvents(t *testing.T) {
	consumed := false
	child := &eventTestWidget{
		onEvent: func() { consumed = true },
	}
	child.SetVisible(true)
	child.SetEnabled(true)

	rb := primitives.NewRepaintBoundary(child)
	rb.SetVisible(false)

	ke := event.NewKeyEvent(event.KeyPress, event.KeyA, 'a', 0)
	rb.Event(nil, ke)

	if consumed {
		t.Error("invisible boundary should not dispatch events")
	}
}

func TestRepaintBoundary_Event_NilChild(t *testing.T) {
	rb := primitives.NewRepaintBoundary(nil)
	ke := event.NewKeyEvent(event.KeyPress, event.KeyA, 'a', 0)
	result := rb.Event(nil, ke)

	if result {
		t.Error("nil child should not consume events")
	}
}

// --- Unmount Tests ---

func TestRepaintBoundary_Unmount_FreesCache(t *testing.T) {
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if !rb.CacheValid() {
		t.Error("cache should be valid after draw")
	}

	rb.Unmount()

	if rb.CacheValid() {
		t.Error("cache should be invalid after Unmount")
	}
	if rb.CacheHits() != 0 {
		t.Error("cache hits should be reset after Unmount")
	}
}

// --- Accessibility Tests ---

func TestRepaintBoundary_Accessibility(t *testing.T) {
	rb := primitives.NewRepaintBoundary(nil, primitives.WithDebugLabel("chart"))

	acc, ok := interface{}(rb).(a11y.Accessible)
	if !ok {
		t.Fatal("RepaintBoundary should implement a11y.Accessible")
	}

	if acc.AccessibilityRole() != a11y.RoleGenericContainer {
		t.Errorf("expected RoleGenericContainer, got %v", acc.AccessibilityRole())
	}
	if acc.AccessibilityLabel() != "chart" {
		t.Errorf("expected label 'chart', got %q", acc.AccessibilityLabel())
	}
	if acc.AccessibilityHint() != "" {
		t.Error("expected empty hint")
	}
	if acc.AccessibilityValue() != "" {
		t.Error("expected empty value")
	}

	state := acc.AccessibilityState()
	if state.Disabled {
		t.Error("should not be disabled by default")
	}
	if state.Hidden {
		t.Error("should not be hidden by default")
	}
	if acc.AccessibilityActions() != nil {
		t.Error("should have no actions")
	}
}

// --- DrawStats Integration Tests ---

func TestRepaintBoundary_Draw_CacheHitIncrementsDrawStats(t *testing.T) {
	// When RepaintBoundary serves from cache, it should increment
	// DrawStats.CachedWidgets via the DrawStatsProvider interface.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx := widget.NewContext()
	var stats widget.DrawStats
	ctx.SetDrawStats(&stats)

	canvas := &imageRecordingCanvas{}

	// First draw: cache miss → child rendered.
	rb.Draw(ctx, canvas)
	if stats.CachedWidgets != 0 {
		t.Errorf("CachedWidgets = %d after first draw, want 0 (cache miss)", stats.CachedWidgets)
	}

	// Second draw: child is clean → cache hit.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2)
	if stats.CachedWidgets != 1 {
		t.Errorf("CachedWidgets = %d after second draw, want 1 (cache hit)", stats.CachedWidgets)
	}

	// Third draw: still clean → another cache hit.
	canvas3 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas3)
	if stats.CachedWidgets != 2 {
		t.Errorf("CachedWidgets = %d after third draw, want 2", stats.CachedWidgets)
	}

	ctx.SetDrawStats(nil)
}

func TestRepaintBoundary_Draw_DirtySubtreeDoesNotIncrementCachedWidgets(t *testing.T) {
	// When the child subtree is dirty, RepaintBoundary re-renders
	// and should NOT increment CachedWidgets.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx := widget.NewContext()
	var stats widget.DrawStats
	ctx.SetDrawStats(&stats)

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas) // First draw (cache miss).

	// Mark child dirty.
	child.SetNeedsRedraw(true)

	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2) // Second draw: dirty child → re-render, not cache hit.

	if stats.CachedWidgets != 0 {
		t.Errorf("CachedWidgets = %d, want 0 (dirty subtree should not count as cache hit)",
			stats.CachedWidgets)
	}

	ctx.SetDrawStats(nil)
}

func TestRepaintBoundary_Draw_NilDrawStatsDoesNotPanic(t *testing.T) {
	// When context does not provide DrawStats (nil), cache hit should
	// still work correctly (just no stats recorded).
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas) // First draw: nil ctx.
	rb.Draw(nil, canvas) // Second draw: cache hit, nil ctx → no panic.

	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1 (cache hit should work without stats)", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_NonProviderContextDoesNotPanic(t *testing.T) {
	// When context does not implement DrawStatsProvider, cache hit
	// should still work (just no stats recorded). Uses a plain Context.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// Use ContextImpl but without SetDrawStats → DrawStats() returns nil.
	ctx := widget.NewContext()

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas) // First draw.
	rb.Draw(ctx, canvas) // Second draw: cache hit.

	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}

func TestRepaintBoundary_DrawViaDrawTree_CachedWidgetsPopulated(t *testing.T) {
	// When drawn through widget.DrawTree, the stats should include
	// CachedWidgets from RepaintBoundary cache hits.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	ctx := widget.NewContext()
	canvas := &imageRecordingCanvas{}

	// First draw: cache miss.
	stats1 := widget.DrawTree(rb, ctx, canvas)
	if stats1.CachedWidgets != 0 {
		t.Errorf("first DrawTree: CachedWidgets = %d, want 0", stats1.CachedWidgets)
	}

	// Second draw: cache hit → CachedWidgets should be 1.
	canvas2 := &imageRecordingCanvas{}
	stats2 := widget.DrawTree(rb, ctx, canvas2)
	if stats2.CachedWidgets != 1 {
		t.Errorf("second DrawTree: CachedWidgets = %d, want 1", stats2.CachedWidgets)
	}
}

// --- DirtyTracker Fast Path Tests (Phase 4, ADR-004) ---

// mockDirtyTracker implements widget.DirtyTrackerRef for testing.
type mockDirtyTracker struct {
	intersectsResult bool
	intersectsCalls  int
	lastBounds       geometry.Rect
}

func (m *mockDirtyTracker) Intersects(bounds geometry.Rect) bool {
	m.intersectsCalls++
	m.lastBounds = bounds
	return m.intersectsResult
}

// dirtyTrackerContext wraps ContextImpl to provide DirtyTrackerProvider.
// In production, ContextImpl itself implements DirtyTrackerProvider.
// This helper ensures tests exercise the same code path.
func newContextWithDirtyTracker(tracker widget.DirtyTrackerRef) *widget.ContextImpl {
	ctx := widget.NewContext()
	ctx.SetDirtyTracker(tracker)
	return ctx
}

func TestRepaintBoundary_Draw_FastPath_NoIntersection_SkipsTreeWalk(t *testing.T) {
	// When the dirty tracker says bounds don't intersect any dirty region,
	// RepaintBoundary should skip NeedsRedrawInTree and serve from cache.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw: populates cache.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)
	if !rb.CacheValid() {
		t.Fatal("cache should be valid after first draw")
	}

	// Set up context with dirty tracker that says NO intersection.
	tracker := &mockDirtyTracker{intersectsResult: false}
	ctx := newContextWithDirtyTracker(tracker)

	// Second draw: fast path should serve from cache.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2)

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1 (fast path should prevent re-render)", child.drawCount)
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
	if tracker.intersectsCalls != 1 {
		t.Errorf("Intersects called %d times, want 1", tracker.intersectsCalls)
	}
	if len(canvas2.drawImageCalls) != 1 {
		t.Errorf("DrawImage called %d times, want 1 (blit from cache)", len(canvas2.drawImageCalls))
	}
}

func TestRepaintBoundary_Draw_FastPath_Intersection_FallsToSlowPath(t *testing.T) {
	// When the dirty tracker says bounds DO intersect a dirty region,
	// RepaintBoundary should fall through to the slow path (NeedsRedrawInTree).
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw: populates cache.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	// Set up context with dirty tracker that says YES intersection.
	tracker := &mockDirtyTracker{intersectsResult: true}
	ctx := newContextWithDirtyTracker(tracker)

	// Child is clean → NeedsRedrawInTree returns false → cache hit via slow path.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2)

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1 (child is clean, slow path cache hit)", child.drawCount)
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1 (slow path cache hit)", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_FastPath_IntersectionAndDirty_ReRendersChild(t *testing.T) {
	// When dirty tracker says intersection AND child is dirty,
	// RepaintBoundary should re-render the child.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	// Mark child dirty.
	child.SetNeedsRedraw(true)

	// Dirty tracker says intersection exists.
	tracker := &mockDirtyTracker{intersectsResult: true}
	ctx := newContextWithDirtyTracker(tracker)

	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2)

	if child.drawCount != 2 {
		t.Errorf("child drawn %d times, want 2 (dirty child should re-render)", child.drawCount)
	}
	if rb.CacheHits() != 0 {
		t.Errorf("CacheHits = %d, want 0 (dirty subtree)", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_FastPath_NoDirtyTrackerProvider_UsesSlowPath(t *testing.T) {
	// When context does not implement DirtyTrackerProvider (nil ctx),
	// RepaintBoundary should use the existing slow path.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw with nil context.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	// Second draw with nil context: slow path cache hit.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(nil, canvas2)

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1 (nil ctx slow path cache hit)", child.drawCount)
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_FastPath_NilTracker_UsesSlowPath(t *testing.T) {
	// When DirtyTrackerProvider returns nil tracker,
	// RepaintBoundary should fall through to slow path.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw.
	ctx := widget.NewContext()
	// Do NOT set dirty tracker — it stays nil.
	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas)

	// Second draw: nil tracker → slow path → cache hit.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2)

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1", child.drawCount)
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_FastPath_RecordsDrawStats(t *testing.T) {
	// Fast path cache hit should increment DrawStats.CachedWidgets.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw: populates cache.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	// Set up context with dirty tracker (no intersection) and DrawStats.
	tracker := &mockDirtyTracker{intersectsResult: false}
	ctx := widget.NewContext()
	ctx.SetDirtyTracker(tracker)
	var stats widget.DrawStats
	ctx.SetDrawStats(&stats)

	// Second draw: fast path.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas2)

	if stats.CachedWidgets != 1 {
		t.Errorf("CachedWidgets = %d, want 1 (fast path cache hit)", stats.CachedWidgets)
	}

	ctx.SetDrawStats(nil)
	ctx.SetDirtyTracker(nil)
}

func TestRepaintBoundary_Draw_FastPath_InvalidCacheIgnoresTracker(t *testing.T) {
	// When cache is not valid, the fast path should be skipped
	// even if the tracker says no intersection.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// Do NOT do a first draw — cache is invalid.
	tracker := &mockDirtyTracker{intersectsResult: false}
	ctx := newContextWithDirtyTracker(tracker)

	canvas := &imageRecordingCanvas{}
	rb.Draw(ctx, canvas)

	// Should have rendered child despite tracker saying no intersection.
	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1 (invalid cache must render)", child.drawCount)
	}
	if rb.CacheHits() != 0 {
		t.Errorf("CacheHits = %d, want 0 (cache was invalid)", rb.CacheHits())
	}
	// Tracker should not have been consulted since cache was invalid.
	if tracker.intersectsCalls != 0 {
		t.Errorf("Intersects called %d times, want 0 (cache invalid, tracker not consulted)",
			tracker.intersectsCalls)
	}
}

func TestRepaintBoundary_Draw_FastPath_MultipleBoundaries_OneDirty(t *testing.T) {
	// End-to-end test: 10+ boundaries, 1 dirty region overlapping only
	// one boundary. Only that boundary should re-render; the rest should
	// serve from cache via the fast path.
	const numBoundaries = 12
	children := make([]*drawCountingWidget, numBoundaries)
	boundaries := make([]*primitives.RepaintBoundary, numBoundaries)

	for i := range numBoundaries {
		children[i] = newDrawCountingWidget()
		boundaries[i] = primitives.NewRepaintBoundary(children[i])
		// Place each boundary at a unique Y offset (0, 50, 100, ...).
		y := float32(i) * 50
		boundaries[i].Layout(nil, geometry.BoxConstraints(0, 200, 0, 50))
		boundaries[i].SetBounds(geometry.NewRect(0, y, 100, y+50))
	}

	// First draw: all boundaries render their children.
	for i := range numBoundaries {
		canvas := &imageRecordingCanvas{}
		boundaries[i].Draw(nil, canvas)
	}

	// Verify all caches are valid and all children drawn exactly once.
	for i := range numBoundaries {
		if !boundaries[i].CacheValid() {
			t.Fatalf("boundary[%d] cache not valid after first draw", i)
		}
		if children[i].drawCount != 1 {
			t.Fatalf("children[%d] drawn %d times, want 1", i, children[i].drawCount)
		}
	}

	// Set up a dirty tracker with a single dirty region at Y=150..200,
	// which overlaps only boundary[3] (bounds: 150..200).
	// Use a more realistic tracker that checks actual spatial overlap.
	dirtyRegion := geometry.NewRect(0, 150, 100, 200)
	tracker := &spatialDirtyTracker{regions: []geometry.Rect{dirtyRegion}}

	// Mark child[3] dirty to force re-render on slow path.
	children[3].SetNeedsRedraw(true)

	ctx := widget.NewContext()
	ctx.SetDirtyTracker(tracker)
	var stats widget.DrawStats
	ctx.SetDrawStats(&stats)

	// Second draw: all boundaries.
	for i := range numBoundaries {
		canvas := &imageRecordingCanvas{}
		boundaries[i].Draw(ctx, canvas)
	}

	// Verify: only child[3] was re-rendered.
	for i := range numBoundaries {
		if i == 3 {
			if children[i].drawCount != 2 {
				t.Errorf("children[%d] drawn %d times, want 2 (dirty region overlap)", i, children[i].drawCount)
			}
		} else {
			if children[i].drawCount != 1 {
				t.Errorf("children[%d] drawn %d times, want 1 (no dirty region overlap)", i, children[i].drawCount)
			}
		}
	}

	// CachedWidgets should be numBoundaries - 1 (all except the dirty one).
	// boundary[3] intersects the dirty region AND its child is dirty, so it re-renders.
	if stats.CachedWidgets != numBoundaries-1 {
		t.Errorf("CachedWidgets = %d, want %d", stats.CachedWidgets, numBoundaries-1)
	}

	ctx.SetDrawStats(nil)
	ctx.SetDirtyTracker(nil)
}

// spatialDirtyTracker is a test helper that performs real spatial intersection
// checks, similar to dirty.Tracker.Intersects. Used for end-to-end tests.
type spatialDirtyTracker struct {
	regions []geometry.Rect
}

func (s *spatialDirtyTracker) Intersects(bounds geometry.Rect) bool {
	for _, r := range s.regions {
		if r.Intersects(bounds) {
			return true
		}
	}
	return false
}

// --- Helper test widgets ---

type eventTestWidget struct {
	widget.WidgetBase
	onEvent func()
}

func (w *eventTestWidget) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(100, 50))
}

func (w *eventTestWidget) Draw(_ widget.Context, _ widget.Canvas) {}

func (w *eventTestWidget) Event(_ widget.Context, _ event.Event) bool {
	if w.onEvent != nil {
		w.onEvent()
	}
	return true
}

var _ widget.Widget = (*eventTestWidget)(nil)

type mouseTrackingWidget struct {
	widget.WidgetBase
	onMouse func(geometry.Point)
}

func (w *mouseTrackingWidget) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(100, 50))
}

func (w *mouseTrackingWidget) Draw(_ widget.Context, _ widget.Canvas) {}

func (w *mouseTrackingWidget) Event(_ widget.Context, e event.Event) bool {
	if me, ok := e.(*event.MouseEvent); ok {
		if w.onMouse != nil {
			w.onMouse(me.Position)
		}
		return true
	}
	return false
}

var _ widget.Widget = (*mouseTrackingWidget)(nil)

// --- GPU Texture Compositing Tests ---

// gpuTextureCanvas is a mock canvas that implements the gpuTextureDrawer
// interface (DrawGPUTexture method). Used to verify that RepaintBoundary
// correctly composites GPU textures via type assertion.
type gpuTextureCanvas struct {
	mockCanvas
	drawGPUTextureCalls []gpuTextureCall
	drawImageCalls      []drawImageCall
}

type gpuTextureCall struct {
	view          any
	x, y          float64
	width, height int
}

func (c *gpuTextureCanvas) DrawGPUTexture(view any, x, y float64, width, height int) {
	c.drawGPUTextureCalls = append(c.drawGPUTextureCalls, gpuTextureCall{
		view: view, x: x, y: y, width: width, height: height,
	})
}

func (c *gpuTextureCanvas) DrawImage(img image.Image, at geometry.Point) {
	c.drawImageCalls = append(c.drawImageCalls, drawImageCall{img: img, at: at})
}

func TestRepaintBoundary_Draw_CPUFallback_WhenGPUUnavailable(t *testing.T) {
	// In test environment there is no GPU device, so renderWithGPUTexture
	// returns false. Verify that the CPU fallback path works correctly.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1", child.drawCount)
	}
	if len(canvas.drawImageCalls) != 1 {
		t.Errorf("DrawImage called %d times, want 1 (CPU fallback)", len(canvas.drawImageCalls))
	}
	if !rb.CacheValid() {
		t.Error("cache should be valid after CPU fallback render")
	}
}

func TestRepaintBoundary_Draw_CPUFallback_CacheHitWorks(t *testing.T) {
	// Verify that CPU fallback cache hit works when GPU is unavailable.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw: cache miss, CPU render.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	// Second draw: cache hit, should use CPU DrawImage (no GPU texture).
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(nil, canvas2)

	if child.drawCount != 1 {
		t.Errorf("child drawn %d times, want 1 (cache hit)", child.drawCount)
	}
	if len(canvas2.drawImageCalls) != 1 {
		t.Errorf("DrawImage called %d times, want 1 (CPU cache hit)", len(canvas2.drawImageCalls))
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_GPUTextureCanvas_FallsBackToCPU(t *testing.T) {
	// Even with a gpuTextureDrawer-capable canvas, if GPU is unavailable
	// (no GPU texture cached), rendering falls back to CPU DrawImage.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &gpuTextureCanvas{}
	rb.Draw(nil, canvas)

	// GPU unavailable in tests → CPU fallback → DrawImage used.
	if len(canvas.drawGPUTextureCalls) != 0 {
		t.Errorf("DrawGPUTexture called %d times, want 0 (no GPU available)", len(canvas.drawGPUTextureCalls))
	}
	if len(canvas.drawImageCalls) != 1 {
		t.Errorf("DrawImage called %d times, want 1 (CPU fallback)", len(canvas.drawImageCalls))
	}
}

func TestRepaintBoundary_Unmount_ResetsGPUState(t *testing.T) {
	// Unmount should clear all cache state including GPU fields.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas) // Populate cache.

	if !rb.CacheValid() {
		t.Fatal("cache should be valid before Unmount")
	}

	rb.Unmount()

	if rb.CacheValid() {
		t.Error("cache should be invalid after Unmount")
	}
	if rb.CacheHits() != 0 {
		t.Error("cache hits should be 0 after Unmount")
	}
}

func TestRepaintBoundary_Layout_SizeChange_InvalidatesCache(t *testing.T) {
	// When size changes, cache should be invalidated.
	// This also tests that GPU texture release happens on size change.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)

	// First layout + draw.
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)
	if !rb.CacheValid() {
		t.Fatal("cache should be valid after first draw")
	}

	// Layout with different size.
	rb.Layout(nil, geometry.Tight(geometry.Sz(50, 25)))
	if rb.CacheValid() {
		t.Error("cache should be invalidated after size change")
	}

	// Draw after size change should re-render.
	canvas2 := &imageRecordingCanvas{}
	rb.Draw(nil, canvas2)
	if child.drawCount != 2 {
		t.Errorf("child drawn %d times, want 2 (re-render after size change)", child.drawCount)
	}
}

func TestRepaintBoundary_Draw_GPUTextureCanvas_CacheHitUsesCPU_WhenNoGPUTexture(t *testing.T) {
	// When GPU texture is not cached but canvas supports DrawGPUTexture,
	// the cache hit should fall back to CPU DrawImage.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw with regular canvas → CPU cache.
	canvas := &imageRecordingCanvas{}
	rb.Draw(nil, canvas)

	// Second draw with GPU-capable canvas → no GPU texture, falls back to CPU.
	canvas2 := &gpuTextureCanvas{}
	rb.Draw(nil, canvas2)

	if len(canvas2.drawGPUTextureCalls) != 0 {
		t.Errorf("DrawGPUTexture called %d times, want 0 (no GPU texture cached)",
			len(canvas2.drawGPUTextureCalls))
	}
	if len(canvas2.drawImageCalls) != 1 {
		t.Errorf("DrawImage called %d times, want 1 (CPU cache hit)", len(canvas2.drawImageCalls))
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}

func TestRepaintBoundary_Draw_FastPath_GPUTexture_FallsBackToCPU(t *testing.T) {
	// Fast path (dirty tracker says no intersection) with GPU-capable canvas
	// but no GPU texture cached → falls back to CPU DrawImage.
	child := newDrawCountingWidget()
	rb := primitives.NewRepaintBoundary(child)
	rb.Layout(nil, geometry.BoxConstraints(0, 200, 0, 100))

	// First draw: populate CPU cache.
	canvas := &gpuTextureCanvas{}
	rb.Draw(nil, canvas)

	// Set up fast path: dirty tracker says no intersection.
	tracker := &mockDirtyTracker{intersectsResult: false}
	ctx := newContextWithDirtyTracker(tracker)

	// Second draw with fast path → no GPU texture → CPU DrawImage.
	canvas2 := &gpuTextureCanvas{}
	rb.Draw(ctx, canvas2)

	if len(canvas2.drawGPUTextureCalls) != 0 {
		t.Errorf("DrawGPUTexture called %d times, want 0 (no GPU texture)", len(canvas2.drawGPUTextureCalls))
	}
	if len(canvas2.drawImageCalls) != 1 {
		t.Errorf("DrawImage called %d times, want 1 (fast path CPU fallback)", len(canvas2.drawImageCalls))
	}
	if rb.CacheHits() != 1 {
		t.Errorf("CacheHits = %d, want 1", rb.CacheHits())
	}
}
