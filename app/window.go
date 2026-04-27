package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/gogpu/gg/scene"
	"github.com/gogpu/gpucontext"
	ui "github.com/gogpu/ui"
	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/internal/dirty"
	ifocus "github.com/gogpu/ui/internal/focus"
	internalRender "github.com/gogpu/ui/internal/render"
	"github.com/gogpu/ui/overlay"
	"github.com/gogpu/ui/state"
	"github.com/gogpu/ui/theme"
	"github.com/gogpu/ui/widget"
)

// dirtyBoundaryEntry tracks a RepaintBoundary that has been marked dirty
// by upward propagation. The key is the boundary's CacheKey for deduplication.
type dirtyBoundaryEntry struct {
	boundary widget.RepaintBoundaryMarker
}

const (
	// defaultWidth is the default window width in headless mode.
	defaultWidth = 800
	// defaultHeight is the default window height in headless mode.
	defaultHeight = 600
	// defaultScale is the default DPI scale factor.
	defaultScale = 1.0
)

// Window manages the widget tree and coordinates layout, draw, and
// event dispatch for a single window.
//
// Window is created by [App] and should not be instantiated directly.
// It is NOT safe for concurrent access.
// animationStopper can stop a continuous render session.
type animationStopper interface {
	Stop()
}

type Window struct {
	root      widget.Widget
	ctx       *widget.ContextImpl
	wp        gpucontext.WindowProvider
	pp        gpucontext.PlatformProvider
	scheduler *state.Scheduler
	theme     *theme.Theme
	overlays  *overlay.Stack
	focusMgr  *ifocus.Manager

	// renderMode controls background ownership and incremental rendering.
	// See RenderMode documentation for details.
	renderMode RenderMode

	// animToken is non-nil while continuous rendering is active for animations.
	animToken animationStopper

	// animIdleFrames counts consecutive frames with no Invalidate call.
	// Used to stop the animation pumper after animations complete.
	animIdleFrames int

	// needsLayout indicates that layout should be recalculated.
	needsLayout bool

	// needsRedraw indicates that the draw pass should be performed.
	// This is set when any widget in the tree needs re-rendering,
	// and cleared after a successful draw. When false, DrawTo can
	// skip rendering entirely because the previous frame's output
	// is still valid in the GPU framebuffer.
	needsRedraw bool

	// needsFullRepaint forces a complete redraw of the entire widget tree
	// on the next DrawTo call. Set on first frame, resize, theme change,
	// and SetRoot. When true, DrawTo clears the entire pixmap and draws
	// all widgets unconditionally instead of using incremental dirty regions.
	needsFullRepaint bool

	// dirtyTracker collects and merges dirty regions for incremental redraw.
	// Populated by dirtyCollector before each DrawTo call.
	dirtyTracker *dirty.Tracker

	// dirtyCollector walks the widget tree to find dirty widgets and records
	// their bounds in dirtyTracker.
	dirtyCollector *dirty.Collector

	// lastDrawStats holds per-widget statistics from the most recent
	// draw traversal. Updated by DrawTo() and the headless draw() path.
	lastDrawStats widget.DrawStats

	// lastDirtyUnion is the union of all dirty regions from the most recent
	// dirty-region-only repaint. Zero when the last frame was a full repaint
	// or frame skip. Used by desktop.Run to pass dirty region to ggcanvas
	// for partial texture upload.
	lastDirtyUnion geometry.Rect

	// lastWasFullRepaint indicates that the most recent DrawTo performed a
	// full repaint (first frame, resize, theme change). When true, the
	// entire pixmap was modified and needs full GPU upload.
	lastWasFullRepaint bool

	// hoveredWidget tracks the widget currently under the mouse pointer.
	// Used for sending MouseEnter/MouseLeave events to individual widgets
	// as the mouse moves across the widget tree.
	hoveredWidget widget.Widget

	// mouseButtonsHeld tracks pressed mouse buttons to prevent cursor
	// reset during drag operations (Frame.ResetCursor skipped while dragging).
	mouseButtonsHeld event.ButtonState

	// windowSize tracks the last known window size in physical pixels.
	windowSize geometry.Size

	// frameCallback, if set, is called after each frame with statistics.
	frameCallback FrameCallback

	// imageCache is the centralized LRU cache for RepaintBoundary pixel
	// buffers. All RepaintBoundary instances in this window share this
	// cache, which enforces a memory budget (default 64MB) and evicts
	// least-recently-used entries. Phase 5, ADR-004.
	imageCache *internalRender.ImageCache

	// dirtyBoundaries collects RepaintBoundary instances marked dirty by
	// upward propagation (ADR-007, Task 1e). Populated by the
	// onBoundaryDirty callback wired during mount. Used by future Phase 2
	// PaintDirtyBoundaries to repaint only changed boundaries.
	dirtyBoundaries map[uint64]dirtyBoundaryEntry
}

// newWindow creates a Window with the given providers.
func newWindow(
	wp gpucontext.WindowProvider,
	pp gpucontext.PlatformProvider,
	scheduler *state.Scheduler,
	t *theme.Theme,
	renderMode RenderMode,
) *Window {
	ctx := widget.NewContext()

	tracker := dirty.NewTracker()
	imgCache := internalRender.DefaultImageCache()
	w := &Window{
		ctx:              ctx,
		wp:               wp,
		pp:               pp,
		scheduler:        scheduler,
		theme:            t,
		renderMode:       renderMode,
		focusMgr:         ifocus.New(nil),
		needsLayout:      true,
		needsFullRepaint: true,
		dirtyTracker:     tracker,
		dirtyCollector:   dirty.NewCollector(tracker),
		imageCache:       imgCache,
		dirtyBoundaries:  make(map[uint64]dirtyBoundaryEntry),
	}

	// Set centralized image cache on context so RepaintBoundary instances
	// use the shared LRU cache with memory budget enforcement (Phase 5).
	ctx.SetImageCache(imgCache)

	// Initialize overlay stack.
	w.overlays = overlay.NewStack(func() {
		w.needsLayout = true
		w.needsRedraw = true
		if w.wp != nil {
			w.wp.RequestRedraw()
		}
	})

	// Set scheduler on context so widgets can bind signals on mount.
	ctx.SetScheduler(scheduler)

	// Set the theme provider on the context so widgets can access theme.
	if t != nil {
		ctx.SetThemeProvider(t)
	}

	// Set initial scale from WindowProvider.
	w.updateScale()

	// Set initial window size.
	w.updateWindowSize()

	// Set overlay manager on context so widgets can push/remove overlays.
	ctx.SetOverlayManager(&windowOverlayManager{window: w})

	// Wire invalidation callback to request redraw.
	// Invalidate = structural change (layout + redraw of entire tree).
	ctx.SetOnInvalidate(func() {
		w.needsLayout = true
		w.needsRedraw = true
		if w.root != nil {
			widget.MarkRedrawInTree(w.root)
		}
		if w.wp != nil {
			w.wp.RequestRedraw()
		}
	})

	// Wire partial invalidation callback.
	// InvalidateRect = visual change in a specific region (redraw only, no layout).
	// Used by widgets with local animations (spinner, progress) that don't
	// affect layout of other widgets.
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {
		w.needsRedraw = true
		if w.wp != nil {
			w.wp.RequestRedraw()
		}
	})

	// Wire scheduler to wake render loop when signals change.
	// Signal dirty = visual content changed (redraw only).
	// Layout is NOT needed — widget size/position unchanged.
	// Widgets that need relayout after signal change call ctx.Invalidate().
	if wp != nil {
		scheduler.SetOnDirty(func() {
			w.needsRedraw = true
			if w.wp != nil {
				w.wp.RequestRedraw()
			}
		})
	}

	return w
}

// SetRoot sets the root widget for this window.
//
// Setting a new root triggers a full layout on the next Frame call.
// The old root tree is unmounted and the new root tree is mounted,
// which triggers signal binding setup/teardown via [widget.Lifecycle].
func (w *Window) SetRoot(root widget.Widget) {
	// Unmount old tree (triggers RepaintBoundary.Unmount which invalidates
	// individual cache entries from the shared ImageCache).
	if w.root != nil {
		widget.UnmountTree(w.root)
	}

	// Clear the entire image cache since the old widget tree is gone and
	// the new tree will have different boundaries with different keys.
	if w.imageCache != nil {
		w.imageCache.InvalidateAll()
	}

	w.root = root
	w.needsLayout = true
	w.needsRedraw = true
	w.needsFullRepaint = true

	// Update focus manager with new root so Tab navigation
	// traverses the correct widget tree.
	w.focusMgr.SetRoot(root)

	// Mount new tree and mark all widgets as needing redraw.
	if w.root != nil {
		widget.MountTree(w.root, w.ctx)
		widget.MarkRedrawInTree(w.root)
	}
}

// Root returns the current root widget, or nil if none is set.
func (w *Window) Root() widget.Widget {
	return w.root
}

// Context returns the widget context used for layout, draw, and events.
func (w *Window) Context() *widget.ContextImpl {
	return w.ctx
}

// Theme returns the window's current theme.
func (w *Window) Theme() *theme.Theme {
	return w.theme
}

// setTheme updates the window's theme and marks layout as dirty.
func (w *Window) setTheme(t *theme.Theme) {
	w.theme = t
	w.ctx.SetThemeProvider(t)
	w.needsLayout = true
	w.needsRedraw = true
	w.needsFullRepaint = true
	if w.root != nil {
		widget.MarkRedrawInTree(w.root)
	}
}

// HandleEvent dispatches a single event to the widget tree.
//
// Events are first offered to the overlay stack (top overlay has priority).
// If no overlay consumes the event (and no modal overlay blocks it),
// key events are offered to the focus manager for Tab/Shift+Tab navigation
// and registered shortcuts. Finally, unconsumed events are propagated to
// the root widget.
func (w *Window) HandleEvent(e event.Event) {
	if w.root == nil || e == nil {
		return
	}

	// Update context time for event processing.
	w.ctx.SetNow(time.Now())

	// Overlays get priority.
	if w.overlays.HandleEvent(w.ctx, e) {
		return
	}

	// Let focus manager intercept Tab/Shift+Tab and shortcuts.
	if ke, ok := e.(*event.KeyEvent); ok {
		w.syncContextFocusToManager()

		if w.focusMgr.HandleKeyEvent(ke) {
			w.syncManagerFocusToContext()
			return
		}
	}

	// Track widget-level hover for MouseEnter/MouseLeave dispatch.
	// Skip hover updates during drag (button pressed) to prevent cursor
	// from resetting when dragging over other widgets (e.g., SplitView divider).
	if me, ok := e.(*event.MouseEvent); ok {
		switch me.MouseType {
		case event.MouseMove:
			if me.Buttons == 0 {
				w.updateHover(me.Position, me.Buttons, me.Modifiers())
			}
		case event.MouseLeave:
			// Mouse left the window entirely — clear hover state.
			// The window-level leave event still propagates to the root below.
			w.clearHover(me.Buttons, me.Modifiers())
		}
	}

	// Track mouse button state for drag cursor protection.
	if me, ok := e.(*event.MouseEvent); ok {
		w.mouseButtonsHeld = me.Buttons
	}

	// Dispatch event to root widget.
	_ = w.root.Event(w.ctx, e)

	// Sync cursor immediately after event dispatch so hover cursor
	// changes are visible without waiting for the next Frame() tick.
	// In event-driven mode (ContinuousRender=false), Frame() only
	// runs when a redraw is needed, but cursor changes from hover
	// don't trigger redraws.
	if w.pp != nil {
		w.syncCursor()
	}

	// After widget tree processes a mouse press, a widget may have called
	// ctx.RequestFocus. Sync that to the focus manager so subsequent
	// Tab navigation starts from the correct position.
	if me, ok := e.(*event.MouseEvent); ok && me.MouseType == event.MousePress {
		w.syncContextFocusToManager()
	}
}

// HandleResize processes a window resize.
//
// This updates the window size and marks layout as needing recalculation.
func (w *Window) HandleResize(width, height int) {
	w.windowSize = geometry.Sz(float32(width), float32(height))
	w.needsLayout = true
	w.needsRedraw = true
	w.needsFullRepaint = true
	if w.root != nil {
		widget.MarkRedrawInTree(w.root)
	}
}

// HandleFocusChange processes a window focus change.
func (w *Window) HandleFocusChange(focused bool) {
	if w.root == nil {
		return
	}

	var focusType event.FocusEventType
	if focused {
		focusType = event.FocusGained
	} else {
		focusType = event.FocusLost
	}

	e := event.NewFocusEvent(focusType)
	_ = w.root.Event(w.ctx, e)

	// Request redraw so widgets can update visual state (e.g. titlebar
	// active/inactive appearance, focus rings).
	w.needsRedraw = true
	if w.wp != nil {
		w.wp.RequestRedraw()
	}
}

// Frame performs one complete frame cycle.
//
// The frame cycle consists of:
//  1. Update time tracking
//  2. Flush pending scheduler updates
//  3. Perform layout if needed
//  4. Draw the widget tree
//  5. Sync cursor state to platform
//  6. Clear invalidation flags
//
// Frame is a no-op if there is no root widget.
func (w *Window) Frame() {
	if w.root == nil {
		return
	}

	frameStart := time.Now()
	didLayout := w.needsLayout

	// Begin frame timing. DeltaTime = time since last BeginFrame.
	w.ctx.BeginFrame(frameStart)

	// Reset cursor for this frame — but not during drag operations.
	// During drag, the dragging widget (SplitView, Slider) sets cursor
	// on every MouseMove; resetting here would flash default between frames.
	if w.mouseButtonsHeld == 0 {
		w.ctx.ResetCursor()
	}

	// Flush pending signal changes (may trigger new dirty marks).
	// The scheduler's flushFn sets persistent needsRedraw flags on widgets.
	const maxReflushes = 2
	for i := 0; i <= maxReflushes; i++ {
		w.scheduler.Flush()
		if w.scheduler.PendingCount() == 0 {
			break
		}
	}

	// Update scale factor (may change between frames on multi-monitor setups).
	w.updateScale()

	// Update window size from provider.
	w.updateWindowSize()

	// Perform layout if needed.
	// Layout changes always require a redraw since widget positions may shift.
	var layoutDur time.Duration
	if w.needsLayout {
		layoutStart := time.Now()
		w.layout()
		layoutDur = time.Since(layoutStart)
		// Clear needsLayout only if no widget re-invalidated during layout.
		// Animations call ctx.Invalidate() from tickAnimation() during layout,
		// which sets needsLayout back to true — we must not clobber that.
		if !w.ctx.IsInvalidated() {
			w.needsLayout = false
		}
		// Layout changes require full redraw since widget positions may shift,
		// making the persistent pixmap invalid (stale pixels at old positions).
		w.needsRedraw = true
		w.needsFullRepaint = true
		widget.MarkRedrawInTree(w.root)
	}

	// Determine if any widget in the tree needs redraw.
	// This check is O(n) in the worst case but short-circuits on first dirty widget.
	if !w.needsRedraw {
		w.needsRedraw = widget.NeedsRedrawInTree(w.root)
	}

	// Draw the widget tree.
	// In hosted mode (wp != nil), DrawTo() is called later by the host
	// application with a real canvas — so we must NOT clear redraw flags here.
	// In headless mode (wp == nil), there is no DrawTo() call, so we collect
	// stats and clear flags ourselves.
	drawStart := time.Now()
	drawSkipped := !w.needsRedraw
	if w.needsRedraw && w.wp == nil {
		w.draw()
		w.needsRedraw = false
	}
	drawDur := time.Since(drawStart)

	// Sync cursor to platform.
	w.syncCursor()

	// Manage continuous rendering for animations.
	// If a widget called Invalidate during this frame (e.g., animation tick),
	// enter continuous (vsync) rendering mode via StartAnimation.
	// When no more animations are active, stop continuous mode.
	// Manage animation frame pumping.
	// Start pumper when any animation is active (Invalidate from tickAnimation).
	// Keep pumper running for a few extra frames to handle animation completion
	// and prevent start/stop thrashing from periodic data updates.
	if w.ctx.IsInvalidated() || !w.ctx.InvalidatedRect().IsEmpty() {
		w.animIdleFrames = 0
		if w.animToken == nil && w.wp != nil {
			w.animToken = newAnimPumper(w.wp)
		}
	} else if w.animToken != nil {
		w.animIdleFrames++
		// Stop pumper after 3 consecutive idle frames (no Invalidate).
		// This handles the case where data sim triggers periodic Invalidate
		// but no animation is running.
		if w.animIdleFrames > 3 {
			w.animToken.Stop()
			w.animToken = nil
			w.animIdleFrames = 0
		}
	}

	// Clear invalidation state.
	w.ctx.ClearInvalidation()

	// Report frame statistics if callback is set.
	if w.frameCallback != nil {
		w.frameCallback(FrameStats{
			FrameStart:      frameStart,
			LayoutDuration:  layoutDur,
			DrawDuration:    drawDur,
			TotalDuration:   time.Since(frameStart),
			LayoutPerformed: didLayout,
			DrawSkipped:     drawSkipped,
			DrawStats:       w.lastDrawStats,
		})
	}
}

// NeedsLayout returns true if layout needs recalculation.
func (w *Window) NeedsLayout() bool {
	return w.needsLayout
}

// NeedsRedraw reports whether any widget in the tree needs re-rendering.
//
// When this returns false, the host application can skip calling [Window.DrawTo]
// and reuse the previous frame's output from the GPU framebuffer. This is the
// primary optimization of retained-mode rendering: idle UIs consume zero CPU
// for rendering.
func (w *Window) NeedsRedraw() bool {
	if w.needsRedraw {
		return true
	}
	return widget.NeedsRedrawInTree(w.root)
}

// LastDrawStats returns the per-widget statistics from the most recent
// draw traversal (either via [Window.DrawTo] or the headless draw path
// inside [Window.Frame]).
//
// When the last frame was skipped (no dirty widgets), the returned stats
// are zero-valued.
func (w *Window) LastDrawStats() widget.DrawStats {
	return w.lastDrawStats
}

// DirtyRegionCount returns the number of dirty regions from the most recent
// DrawTo call. This reflects the state after Collector.Collect + Tracker.Optimize
// in the last frame.
//
// Returns 0 when the last frame was a full repaint (no incremental regions)
// or when the frame was skipped (nothing dirty).
//
// This is an observability hook for monitoring incremental rendering efficiency.
func (w *Window) DirtyRegionCount() int {
	return w.dirtyTracker.RegionCount()
}

// LastDirtyUnion returns the union of all dirty regions from the most
// recent dirty-region-only repaint. Returns a zero Rect when the last
// frame was a full repaint or a frame skip.
//
// Used by desktop.Run to pass the dirty region to ggcanvas for partial
// texture upload — only the modified region is uploaded to the GPU
// instead of the entire pixmap.
func (w *Window) LastDirtyUnion() geometry.Rect {
	return w.lastDirtyUnion
}

// WasFullRepaint returns true if the most recent DrawTo performed a full
// repaint (first frame, resize, theme change, SetRoot). When true, the
// entire pixmap was modified and needs full GPU upload.
func (w *Window) WasFullRepaint() bool {
	return w.lastWasFullRepaint
}

// WindowSize returns the current window size in logical pixels.
func (w *Window) WindowSize() geometry.Size {
	return w.windowSize
}

// syncManagerFocusToContext updates the widget context's focused widget
// to match the focus manager's state. Called after the focus manager
// moves focus via Tab/Shift+Tab navigation.
func (w *Window) syncManagerFocusToContext() {
	focused := w.focusMgr.Focused()
	if focused == nil {
		// Focus manager cleared focus.
		current := w.ctx.FocusedWidget()
		if current != nil {
			w.ctx.ReleaseFocus(current)
		}
		return
	}

	// The Focusable interface doesn't embed Widget, but in practice all
	// focusable widgets implement Widget (they embed WidgetBase). Use
	// type assertion to get the Widget interface for the context.
	if fw, ok := focused.(widget.Widget); ok {
		w.ctx.RequestFocus(fw)
		w.needsRedraw = true
		if w.wp != nil {
			w.wp.RequestRedraw()
		}
	}
}

// syncContextFocusToManager updates the focus manager's state to match
// the widget context. Called before Tab processing so navigation starts
// from the widget that received focus via mouse click or programmatic
// ctx.RequestFocus.
func (w *Window) syncContextFocusToManager() {
	ctxFocused := w.ctx.FocusedWidget()
	mgrFocused := w.focusMgr.Focused()

	// Check if they already agree.
	if ctxFocused == nil && mgrFocused == nil {
		return
	}

	// If context has a focused widget, sync it to the manager.
	if ctxFocused != nil {
		if f, ok := ctxFocused.(widget.Focusable); ok {
			if mgrFocused != f {
				w.focusMgr.Focus(f)
			}
			return
		}
	}

	// Context has no focus (or non-focusable widget); clear manager focus.
	if mgrFocused != nil {
		w.focusMgr.Blur()
	}
}

// layout performs the layout pass on the widget tree and overlays.
func (w *Window) layout() {
	if w.root == nil {
		return
	}

	// Update window size on context so widgets can access it.
	w.ctx.SetWindowSize(w.windowSize)

	// Create tight constraints matching the window size.
	constraints := geometry.Tight(w.windowSize)

	// Layout the root widget.
	size := w.root.Layout(w.ctx, constraints)

	// Set root bounds to fill the window from origin.
	if setter, ok := w.root.(interface{ SetBounds(geometry.Rect) }); ok {
		setter.SetBounds(geometry.NewRect(0, 0, size.Width, size.Height))
	}

	// Layout overlays with window-sized constraints.
	w.overlays.Layout(w.ctx, w.windowSize)

	// Refresh focus manager's root after layout so the focusable
	// widget list reflects any tree changes (widgets added/removed,
	// visibility/enabled state changes).
	w.focusMgr.SetRoot(w.root)
}

// draw performs the draw pass on the widget tree in headless mode.
func (w *Window) draw() {
	if w.root == nil {
		return
	}

	// Headless mode: no canvas available, so we collect statistics and
	// clear redraw flags without actually calling Draw on widgets.
	// Real rendering happens via DrawTo() when the host provides a canvas.
	w.lastDrawStats = widget.CollectDrawStats(w.root)
	widget.ClearRedrawInTree(w.root)
}

// RenderMode returns the window's current rendering mode.
func (w *Window) RenderMode() RenderMode {
	return w.renderMode
}

// SetRenderMode changes the window's rendering mode at runtime.
//
// Switching to [RenderModeFrameworkManaged] enables frame skip and
// dirty-region rendering. Switching to [RenderModeHostManaged] restores
// full-tree draw every frame.
//
// A full repaint is forced after the switch to ensure the persistent
// pixmap is populated correctly in the new mode.
func (w *Window) SetRenderMode(mode RenderMode) {
	w.renderMode = mode
	w.needsFullRepaint = true
	if w.root != nil {
		widget.MarkRedrawInTree(w.root)
	}
}

// DrawTo performs the draw pass using the provided canvas.
//
// Behavior depends on the [RenderMode]:
//
// In RenderModeHostManaged (default):
//   - The host draws background before calling DrawTo.
//   - DrawTo does NOT call canvas.Clear (host already painted background).
//   - DrawTo always draws the full widget tree and returns true.
//   - dirty.Tracker is populated for RepaintBoundary Intersects fast path.
//
// In RenderModeFrameworkManaged:
//   - Level 1 (Frame Skip): If nothing changed since the last draw,
//     returns false immediately — zero CPU, zero GPU upload.
//   - Level 2 (Dirty Region Redraw): Only dirty regions are redrawn on
//     the persistent pixmap. Clean regions retain previous pixels
//     (Qt QBackingStore pattern).
//   - Full Repaint: On first frame, resize, theme change, or SetRoot,
//     the entire pixmap is cleared and redrawn.
//
// Returns true if rendering was performed, false if the draw was skipped.
func (w *Window) DrawTo(canvas widget.Canvas) bool {
	if w.root == nil || canvas == nil {
		return false
	}

	// Collect dirty regions (always — for RepaintBoundary Intersects fast path).
	w.dirtyTracker.Reset()
	w.dirtyCollector.Collect(w.root)
	w.dirtyTracker.Optimize()

	var drawn bool
	switch w.renderMode {
	case RenderModeFrameworkManaged:
		drawn = w.drawFrameworkManaged(canvas)
	default:
		drawn = w.drawHostManaged(canvas)
	}

	if drawn {
		ui.Logger().LogAttrs(context.Background(), slog.LevelDebug,
			"DrawTo: rendered",
			slog.Int("dirty", w.lastDrawStats.DirtyWidgets),
			slog.Int("clean", w.lastDrawStats.CleanWidgets),
			slog.Int("cached", w.lastDrawStats.CachedWidgets),
			slog.Int("total", w.lastDrawStats.TotalWidgets),
			slog.Int("dirtyRegions", w.dirtyTracker.RegionCount()),
			slog.String("mode", w.renderMode.String()),
		)
	}

	return drawn
}

// drawHostManaged draws the full widget tree without clearing the canvas.
// The host draws background before calling DrawTo.
//
// Dirty-region optimization is handled by RepaintBoundary: clean boundaries
// serve cached GPU textured quads (cheap blit), dirty boundaries re-render
// their subtree. The dirty.Tracker feeds RepaintBoundary.Intersects for
// O(regions) fast-path cache validation instead of O(tree_depth) walks.
//
// Canvas-level dirty clip is NOT used here because the host draws full
// background every frame — clipping would prevent clean widgets from
// being redrawn over the fresh background.
func (w *Window) drawHostManaged(canvas widget.Canvas) bool {
	w.ctx.SetDirtyTracker(w.dirtyTracker)
	defer w.ctx.SetDirtyTracker(nil)

	w.lastDrawStats = widget.DrawTree(w.root, w.ctx, canvas)

	w.overlays.Draw(w.ctx, canvas)
	widget.ClearRedrawInTree(w.root)
	w.needsRedraw = false
	w.needsFullRepaint = false
	return true
}

// drawFrameworkManaged implements the three-level incremental rendering
// pipeline (ADR-004). The framework owns the pixmap lifecycle and draws
// the theme background when needed.
//
// Level 1 (frame skip): if nothing changed and the persistent pixmap is
// valid, returns false immediately -- zero CPU, zero GPU upload.
//
// Level 2 (dirty-region-only repaint): only the regions that changed are
// cleared and redrawn on the persistent pixmap. Clean regions retain the
// previous frame's pixels (Qt QBackingStore / Win32 partial-paint pattern).
//
// Full repaint: on first frame, resize, theme change, SetRoot, layout
// change, or when too many dirty regions accumulate (>16), the entire
// pixmap is cleared and redrawn.
func (w *Window) drawFrameworkManaged(canvas widget.Canvas) bool {
	hasTreeDirty := !w.dirtyTracker.IsEmpty() || w.needsRedraw || widget.NeedsRedrawInTree(w.root)

	// Level 1: Frame skip — nothing changed and pixmap is persistent.
	if !hasTreeDirty && !w.needsFullRepaint {
		return false
	}

	// Clear redraw flags BEFORE draw so flags set during Draw (e.g.,
	// spinner's SetNeedsRedraw(true)) survive until the next frame's
	// collector run. If cleared AFTER draw, the collector on the next
	// frame sees NeedsRedraw=false and misses the widget.
	widget.ClearRedrawInTree(w.root)

	w.ctx.SetDirtyTracker(w.dirtyTracker)
	defer w.ctx.SetDirtyTracker(nil)

	// Reset dirty union from previous frame.
	w.lastDirtyUnion = geometry.Rect{}
	w.lastWasFullRepaint = false

	// Full repaint on first frame, resize, theme change, SetRoot,
	// layout change, or when too many dirty regions accumulate.
	if w.needsFullRepaint || w.dirtyTracker.NeedsFullRepaint() {
		w.drawFullRepaint(canvas)
	} else {
		// Level 2: Dirty-region-only repaint.
		w.drawDirtyRegions(canvas)
	}

	w.overlays.Draw(w.ctx, canvas)
	w.needsRedraw = false
	w.needsFullRepaint = false
	return true
}

// drawFullRepaint clears the entire canvas and redraws the full widget tree.
// Used in FrameworkManaged mode on first frame, resize, theme change, and SetRoot.
func (w *Window) drawFullRepaint(canvas widget.Canvas) {
	bg := w.ThemeBackground()
	canvas.Clear(bg)

	w.lastDrawStats = widget.DrawTree(w.root, w.ctx, canvas)
	w.lastWasFullRepaint = true
}

// drawDirtyRegions clears and redraws only the dirty regions.
// Clean regions retain previous frame's pixels (persistent pixmap).
// Follows the Qt QBackingStore partial-paint pattern.
//
// The algorithm:
//  1. Compute the union of all dirty regions for a single clip rect.
//  2. Clear each dirty region with the theme background.
//  3. Clip to the dirty union and draw the full tree. Widgets outside
//     the clip early-exit from visibility checks, so only widgets
//     overlapping dirty regions actually render.
func (w *Window) drawDirtyRegions(canvas widget.Canvas) {
	bg := w.ThemeBackground()
	regions := w.dirtyTracker.DirtyRegions()
	if len(regions) == 0 {
		return
	}

	// Compute union of all dirty regions for a single clip.
	union := regions[0].Bounds
	for i := 1; i < len(regions); i++ {
		union = union.Union(regions[i].Bounds)
	}

	// Clear dirty regions with theme background using CPU-only fill.
	// DrawRect would trigger the GPU SDF accelerator, queuing SDF shapes
	// on the compositor canvas and blocking the non-MSAA blit-only fast
	// path (ADR-016). FillRectDirect writes directly to the CPU pixmap.
	for _, region := range regions {
		canvas.FillRectDirect(region.Bounds, bg)
	}

	// Clip to dirty union and draw tree.
	// Widgets outside the clip early-exit from isVisible checks,
	// so only widgets overlapping dirty regions actually render.
	canvas.PushClip(union)
	w.lastDrawStats = widget.DrawTree(w.root, w.ctx, canvas)
	canvas.PopClip()

	w.lastDirtyUnion = union
}

// ThemeBackground returns the window background color from the current theme.
// Falls back to white if no theme is configured.
func (w *Window) ThemeBackground() widget.Color {
	if w.theme != nil {
		return w.theme.Colors.Background
	}
	return widget.ColorWhite
}

// syncCursor forwards the cursor state to the platform provider.
func (w *Window) syncCursor() {
	if w.pp == nil {
		return
	}

	cursor := w.ctx.Cursor()
	w.pp.SetCursor(widgetCursorToPlatform(cursor))
}

// updateScale reads the scale factor from the WindowProvider and
// updates the context.
func (w *Window) updateScale() {
	scale := float32(defaultScale)
	if w.wp != nil {
		scale = float32(w.wp.ScaleFactor())
	}
	w.ctx.SetScale(scale)
}

// updateWindowSize reads the window size from the WindowProvider.
func (w *Window) updateWindowSize() {
	if w.wp != nil {
		width, height := w.wp.Size()
		newSize := geometry.Sz(float32(width), float32(height))
		if newSize != w.windowSize {
			w.windowSize = newSize
			w.needsLayout = true
		}
	} else if w.windowSize.Width == 0 && w.windowSize.Height == 0 {
		// Default size for headless mode.
		w.windowSize = geometry.Sz(defaultWidth, defaultHeight)
	}
}

// Overlays returns the window's overlay stack.
func (w *Window) Overlays() *overlay.Stack {
	return w.overlays
}

// FocusManager returns the window's focus manager.
//
// The focus manager handles Tab/Shift+Tab navigation between focusable
// widgets and supports registering global keyboard shortcuts.
func (w *Window) FocusManager() *ifocus.Manager {
	return w.focusMgr
}

// windowOverlayManager adapts the Window's overlay.Stack to the
// widget.OverlayManager interface. This avoids circular imports since
// the widget package cannot import the overlay package.
type windowOverlayManager struct {
	window *Window
}

// PushOverlay wraps the widget in an overlay.Container and pushes it.
func (m *windowOverlayManager) PushOverlay(w widget.Widget, onDismiss func()) {
	container := overlay.NewContainer(w, m.window.windowSize,
		overlay.WithOnDismiss(func() {
			if onDismiss != nil {
				onDismiss()
			}
		}),
	)
	m.window.overlays.Push(container)
}

// PopOverlay removes the topmost overlay.
func (m *windowOverlayManager) PopOverlay() {
	m.window.overlays.Pop()
}

// RemoveOverlay finds and removes the overlay containing the given widget.
func (m *windowOverlayManager) RemoveOverlay(w widget.Widget) {
	for _, o := range m.window.overlays.List() {
		if c, ok := o.(*overlay.Container); ok {
			if c.Content() == w {
				m.window.overlays.Remove(o)
				return
			}
		}
	}
}

// Compile-time check.
var _ widget.OverlayManager = (*windowOverlayManager)(nil)

// updateHover performs hit-testing to find the widget under the mouse and
// sends MouseEnter/MouseLeave events to individual widgets as the mouse
// moves across the widget tree.
//
// This uses ScreenBounds (computed during the Draw pass) for correct
// coordinate mapping, which accounts for scroll offsets, box positions,
// and all parent transforms.
func (w *Window) updateHover(pos geometry.Point, buttons event.ButtonState, mods event.Modifiers) {
	target := hitTest(w.root, pos)
	if target == w.hoveredWidget {
		return
	}

	// Send MouseLeave to the old hovered widget.
	if w.hoveredWidget != nil {
		leave := event.NewMouseEvent(
			event.MouseLeave,
			event.ButtonNone,
			buttons,
			pos, pos,
			mods,
		)
		_ = w.hoveredWidget.Event(w.ctx, leave)
	}

	// Send MouseEnter to the new hovered widget.
	if target != nil {
		enter := event.NewMouseEvent(
			event.MouseEnter,
			event.ButtonNone,
			buttons,
			pos, pos,
			mods,
		)
		_ = target.Event(w.ctx, enter)
	}

	w.hoveredWidget = target
}

// clearHover sends MouseLeave to the currently hovered widget and clears
// the hover state. Called when the mouse leaves the window entirely.
func (w *Window) clearHover(buttons event.ButtonState, mods event.Modifiers) {
	if w.hoveredWidget == nil {
		return
	}

	leave := event.NewMouseEvent(
		event.MouseLeave,
		event.ButtonNone,
		buttons,
		geometry.Point{}, geometry.Point{},
		mods,
	)
	_ = w.hoveredWidget.Event(w.ctx, leave)
	w.hoveredWidget = nil
}

// HoveredWidget returns the widget currently under the mouse pointer,
// or nil if no widget is hovered.
func (w *Window) HoveredWidget() widget.Widget {
	return w.hoveredWidget
}

// hitTest walks the widget tree depth-first and returns the deepest
// visible widget whose ScreenBounds contains the given position.
//
// Children are checked in reverse order (top-most first in z-order)
// so that overlapping widgets receive events correctly.
// Returns nil if no widget contains the point.
func hitTest(root widget.Widget, pos geometry.Point) widget.Widget {
	if root == nil {
		return nil
	}
	return hitTestRecursive(root, pos)
}

// hitTestRecursive performs the recursive depth-first search.
func hitTestRecursive(w widget.Widget, pos geometry.Point) widget.Widget {
	// Check visibility — invisible widgets don't receive hover events.
	if base, ok := w.(interface{ IsVisible() bool }); ok && !base.IsVisible() {
		return nil
	}

	// Check if this widget's ScreenBounds contains the position.
	if sb, ok := w.(interface{ ScreenBounds() geometry.Rect }); ok {
		bounds := sb.ScreenBounds()
		if !bounds.Contains(pos) {
			return nil
		}
	}

	// Check children in reverse order (topmost first).
	children := w.Children()
	for i := len(children) - 1; i >= 0; i-- {
		child := children[i]
		if hit := hitTestRecursive(child, pos); hit != nil {
			return hit
		}
	}

	// No child hit — this widget itself is the target.
	return w
}

// widgetCursorToPlatform converts a widget.CursorType to gpucontext.CursorShape.
func widgetCursorToPlatform(c widget.CursorType) gpucontext.CursorShape {
	switch c {
	case widget.CursorDefault:
		return gpucontext.CursorDefault
	case widget.CursorPointer:
		return gpucontext.CursorPointer
	case widget.CursorText:
		return gpucontext.CursorText
	case widget.CursorCrosshair:
		return gpucontext.CursorCrosshair
	case widget.CursorMove:
		return gpucontext.CursorMove
	case widget.CursorResizeNS:
		return gpucontext.CursorResizeNS
	case widget.CursorResizeEW:
		return gpucontext.CursorResizeEW
	case widget.CursorResizeNESW:
		return gpucontext.CursorResizeNESW
	case widget.CursorResizeNWSE:
		return gpucontext.CursorResizeNWSE
	case widget.CursorNotAllowed:
		return gpucontext.CursorNotAllowed
	case widget.CursorWait:
		return gpucontext.CursorWait
	case widget.CursorNone:
		return gpucontext.CursorNone
	default:
		return gpucontext.CursorDefault
	}
}

// --- Dirty Boundary Management (ADR-007, Task 1e) ---

// AddDirtyBoundary registers a RepaintBoundary as dirty. Called by the
// onBoundaryDirty callback during upward dirty propagation.
//
// The key parameter is the boundary's unique cache key for deduplication.
// If the boundary is already in the set, this is a no-op.
func (w *Window) AddDirtyBoundary(key uint64, boundary widget.RepaintBoundaryMarker) {
	if w.dirtyBoundaries == nil {
		w.dirtyBoundaries = make(map[uint64]dirtyBoundaryEntry)
	}
	w.dirtyBoundaries[key] = dirtyBoundaryEntry{boundary: boundary}
}

// HasDirtyBoundaries reports whether any RepaintBoundary has been marked
// dirty since the last paint pass.
func (w *Window) HasDirtyBoundaries() bool {
	return len(w.dirtyBoundaries) > 0
}

// DirtyBoundaryCount returns the number of dirty RepaintBoundary instances.
func (w *Window) DirtyBoundaryCount() int {
	return len(w.dirtyBoundaries)
}

// ClearDirtyBoundaries resets the dirty boundary set after painting.
// Each boundary's ClearBoundaryDirty is NOT called here — that is the
// responsibility of the PaintDirtyBoundaries method.
func (w *Window) ClearDirtyBoundaries() {
	// Clear map efficiently: delete all entries but keep the allocated map.
	for k := range w.dirtyBoundaries {
		delete(w.dirtyBoundaries, k)
	}
}

// PaintDirtyBoundaries clears the dirty boundary set after a frame.
//
// RepaintBoundary.Draw() handles cache invalidation internally: when
// boundaryDirty is true, it re-records child.Draw() into its scene.Scene.
// This method does NOT need to pre-paint — the Draw pass during
// ComposeRootScene triggers re-recording automatically.
//
// This is the Flutter flushPaint pattern: only dirty RepaintBoundary nodes
// re-record, clean ones replay cached scenes.
func (w *Window) PaintDirtyBoundaries() {
	w.ClearDirtyBoundaries()
}

// ComposeRootScene builds a root scene.Scene by drawing the widget tree
// into a SceneCanvas. RepaintBoundary widgets with cache hits replay their
// cached scene via Scene.Append; dirty boundaries re-record and then replay.
//
// The returned scene contains ALL drawing commands for the entire frame
// (background + widgets + overlays). The compositor renders it via
// GPUSceneRenderer → FlushGPUWithView in a single render pass.
//
// This is ADR-007 Phase 5: scene composition.
func (w *Window) ComposeRootScene() *scene.Scene {
	if w.root == nil {
		return nil
	}

	hasTreeDirty := w.needsRedraw || w.HasDirtyBoundaries() ||
		!w.dirtyTracker.IsEmpty() || widget.NeedsRedrawInTree(w.root)

	if !hasTreeDirty && !w.needsFullRepaint {
		return nil
	}

	size := w.windowSize
	sw := int(size.Width)
	sh := int(size.Height)
	if sw <= 0 || sh <= 0 {
		return nil
	}

	widget.ClearRedrawInTree(w.root)

	rootScene := scene.NewScene()
	rootScene.Reset()

	recorder := internalRender.NewSceneCanvas(rootScene, sw, sh)

	bg := w.ThemeBackground()
	recorder.Clear(bg)

	widget.DrawTree(w.root, w.ctx, recorder)

	w.overlays.Draw(w.ctx, recorder)

	recorder.Close()

	w.needsRedraw = false
	w.needsFullRepaint = false

	return rootScene
}

// BoundaryDamageRegion computes the union of screen bounds of all dirty
// RepaintBoundary instances. This provides a tighter damage region for
// the compositor when only specific boundaries changed (ADR-007 Phase 3,
// Task 3d).
//
// Returns a zero Rect when no boundaries are dirty.
//
// The compositor can use min(BoundaryDamageRegion, LastDirtyUnion) to
// get the tightest possible damage region for scissored GPU present.
func (w *Window) BoundaryDamageRegion() geometry.Rect {
	if len(w.dirtyBoundaries) == 0 {
		return geometry.Rect{}
	}

	var union geometry.Rect
	first := true
	for _, entry := range w.dirtyBoundaries {
		bounds := boundaryScreenBounds(entry.boundary)
		if bounds.IsEmpty() {
			continue
		}
		if first {
			union = bounds
			first = false
		} else {
			union = union.Union(bounds)
		}
	}

	return union
}

// boundaryScreenBounds extracts the screen bounds from a RepaintBoundaryMarker.
// Uses ScreenBounds() if available (computed during Draw), falls back to Bounds().
func boundaryScreenBounds(b widget.RepaintBoundaryMarker) geometry.Rect {
	type screenBounder interface {
		ScreenBounds() geometry.Rect
	}
	if sb, ok := b.(screenBounder); ok {
		r := sb.ScreenBounds()
		if !r.IsEmpty() {
			return r
		}
	}

	type bounder interface {
		Bounds() geometry.Rect
	}
	if bb, ok := b.(bounder); ok {
		return bb.Bounds()
	}

	return geometry.Rect{}
}

// HasDirtyBoundariesOrNeedsRedraw reports whether any rendering work is
// needed: either dirty boundaries from upward propagation or full-frame
// redraw flags (needsRedraw, needsFullRepaint).
func (w *Window) HasDirtyBoundariesOrNeedsRedraw() bool {
	return w.HasDirtyBoundaries() || w.needsRedraw || w.needsFullRepaint
}

// animPumper pumps frames at ~60fps for smooth animation.
// Stopped when animation completes.
type animPumper struct {
	stop chan struct{}
}

func newAnimPumper(wp gpucontext.WindowProvider) *animPumper {
	p := &animPumper{stop: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(16 * time.Millisecond) // ~60fps
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				wp.RequestRedraw()
			}
		}
	}()
	return p
}

func (p *animPumper) Stop() {
	select {
	case p.stop <- struct{}{}:
	default:
	}
}
