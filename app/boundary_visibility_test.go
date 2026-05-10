package app

import (
	"testing"

	"github.com/gogpu/gg/scene"
	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/widget"

	internalRender "github.com/gogpu/ui/internal/render"
)

// animatedBoundary is a test boundary widget that tracks Draw calls
// and simulates ScheduleAnimationFrame behavior (like spinner).
type animatedBoundary struct {
	widget.WidgetBase
	drawCount              int
	scheduleAnimationCalls int
}

func (w *animatedBoundary) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(48, 48))
}

func (w *animatedBoundary) Draw(ctx widget.Context, canvas widget.Canvas) {
	w.drawCount++
	canvas.DrawRect(w.Bounds(), widget.RGBA8(255, 0, 0, 255))
	// Simulate spinner: request next animation frame.
	if ctx != nil {
		if sched, ok := ctx.(widget.AnimationScheduler); ok {
			sched.ScheduleAnimationFrame()
			w.scheduleAnimationCalls++
		}
	}
}

func (w *animatedBoundary) Event(_ widget.Context, _ event.Event) bool { return false }
func (w *animatedBoundary) Children() []widget.Widget                  { return nil }

// dirtyNonBoundary is a non-boundary widget that can be marked dirty
// (simulates LineChart/ProgressBar receiving data ticks).
type dirtyNonBoundary struct {
	widget.WidgetBase
	drawCount int
}

func (w *dirtyNonBoundary) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(800, 150))
}

func (w *dirtyNonBoundary) Draw(_ widget.Context, canvas widget.Canvas) {
	w.drawCount++
	canvas.DrawRect(w.Bounds(), widget.RGBA8(0, 0, 255, 255))
}

func (w *dirtyNonBoundary) Event(_ widget.Context, _ event.Event) bool { return false }
func (w *dirtyNonBoundary) Children() []widget.Widget                  { return nil }

// --- isBoundaryVisible tests ---

func TestIsBoundaryVisible_NoClip_AlwaysVisible(t *testing.T) {
	// Root boundary: no CompositorClip → always visible.
	root := &testLeaf{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	if !isBoundaryVisible(root) {
		t.Error("boundary without CompositorClip should always be visible (root)")
	}
}

func TestIsBoundaryVisible_InsideClip_Visible(t *testing.T) {
	// Spinner at screen (100,200), size 48×48, viewport clip (0,0,800,600).
	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))

	if !isBoundaryVisible(spinner) {
		t.Error("boundary inside CompositorClip should be visible")
	}
}

func TestIsBoundaryVisible_OutsideClip_Invisible(t *testing.T) {
	// Spinner at screen (100,800) — below viewport clip (0,0,800,600).
	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 800, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 800))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))

	if isBoundaryVisible(spinner) {
		t.Error("boundary outside CompositorClip should NOT be visible")
	}
}

func TestIsBoundaryVisible_PartiallyOverlapping_Visible(t *testing.T) {
	// Spinner at screen (780,580) — partially inside viewport (0,0,800,600).
	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(780, 580, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(780, 580))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))

	if !isBoundaryVisible(spinner) {
		t.Error("boundary partially inside CompositorClip should be visible")
	}
}

func TestIsBoundaryVisible_AboveClip_Invisible(t *testing.T) {
	// Spinner scrolled above viewport: screen (100,-100), clip (0,50,800,600).
	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 0, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, -100))
	spinner.SetCompositorClip(geometry.NewRect(0, 50, 800, 600))

	if isBoundaryVisible(spinner) {
		t.Error("boundary above CompositorClip should NOT be visible")
	}
}

// --- PaintBoundaryLayers offscreen culling tests ---

func setupSceneRecorder(t *testing.T) func() {
	t.Helper()
	prev := widget.GetSceneRecorderFactory()
	widget.RegisterSceneRecorder(func(s *scene.Scene, w, h int) (widget.Canvas, func()) {
		rec := internalRender.NewSceneCanvas(s, w, h)
		return rec, rec.Close
	})
	return func() { widget.RegisterSceneRecorder(prev) }
}

func TestPaintBoundaryLayers_SkipsOffscreenBoundary(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	// Root boundary (always visible).
	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	// Spinner offscreen: below viewport.
	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 700, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 700))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})

	PaintBoundaryLayersWithContext(root, nil, ctx)

	if root.CachedScene() == nil {
		t.Error("root boundary should have cached scene (always visible)")
	}
	if spinner.drawCount != 0 {
		t.Errorf("offscreen spinner Draw should NOT be called, got %d calls", spinner.drawCount)
	}
	if spinner.CachedScene() != nil {
		t.Error("offscreen spinner should NOT have cached scene (recording skipped)")
	}
	if !spinner.IsSceneDirty() {
		t.Error("offscreen spinner scene should remain dirty (for re-record when scrolled into view)")
	}
}

func TestPaintBoundaryLayers_RecordsVisibleBoundary(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	// Spinner inside viewport.
	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})

	PaintBoundaryLayersWithContext(root, nil, ctx)

	if spinner.drawCount == 0 {
		t.Error("visible spinner Draw should be called during recording")
	}
	if spinner.CachedScene() == nil {
		t.Error("visible spinner should have cached scene after recording")
	}
	if spinner.IsSceneDirty() {
		t.Error("visible spinner scene should be clean after recording")
	}
}

func TestPaintBoundaryLayers_OffscreenNoScheduleAnimation(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	// Spinner offscreen.
	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 700, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 700))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	animFrameCount := 0
	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})
	ctx.SetOnScheduleAnimation(func() { animFrameCount++ })

	PaintBoundaryLayersWithContext(root, nil, ctx)

	if animFrameCount != 0 {
		t.Errorf("offscreen spinner should NOT trigger ScheduleAnimationFrame, got %d calls",
			animFrameCount)
	}
	if spinner.scheduleAnimationCalls != 0 {
		t.Errorf("offscreen spinner Draw should not run → 0 ScheduleAnimationFrame calls, got %d",
			spinner.scheduleAnimationCalls)
	}
}

func TestPaintBoundaryLayers_VisibleSchedulesAnimation(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	// Spinner inside viewport.
	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	animFrameCount := 0
	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})
	ctx.SetOnScheduleAnimation(func() { animFrameCount++ })

	PaintBoundaryLayersWithContext(root, nil, ctx)

	if spinner.scheduleAnimationCalls == 0 {
		t.Error("visible spinner Draw should call ScheduleAnimationFrame")
	}
}

// --- Damage rect screen-space tests ---

func TestOnBoundaryDirty_UsesScreenCoords(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	// Spinner at screen position (200,300), size 48×48.
	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(200, 300, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(200, 300))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	var damageRect geometry.Rect
	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(r geometry.Rect) {
		damageRect = r
	})

	// First: record to wire onBoundaryDirty callback.
	PaintBoundaryLayersWithContext(root, nil, ctx)

	// Trigger onBoundaryDirty by invalidating the scene.
	spinner.InvalidateScene()

	// Damage rect should be in screen coordinates: Min=(200,300), Max=(248,348).
	// NOT local bounds origin (200,300,248,348) which is Rect{(200,300),(248,348)}.
	wantMin := geometry.Pt(200, 300)
	wantMax := geometry.Pt(248, 348)
	if damageRect.Min != wantMin || damageRect.Max != wantMax {
		t.Errorf("damage rect = %v (Min=%v, Max=%v), want Min=%v Max=%v",
			damageRect, damageRect.Min, damageRect.Max, wantMin, wantMax)
	}
}

func TestOnBoundaryDirty_RootDamageAtOrigin(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	// Root boundary at (0,0), size 800×600.
	root := &testLeaf{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	var damageRect geometry.Rect
	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(r geometry.Rect) {
		damageRect = r
	})

	// Record to wire callback.
	PaintBoundaryLayersWithContext(root, nil, ctx)

	root.InvalidateScene()

	wantMin := geometry.Pt(0, 0)
	wantMax := geometry.Pt(800, 600)
	if damageRect.Min != wantMin || damageRect.Max != wantMax {
		t.Errorf("root damage rect = %v, want Min=%v Max=%v", damageRect, wantMin, wantMax)
	}
}

// --- Non-boundary dirty propagation tests ---

func TestNonBoundaryDirty_ForcesRootReRecord(t *testing.T) {
	// When a non-boundary widget (chart) is dirty and parent chain is broken,
	// NeedsRedrawInTreeNonBoundary should find it → root re-records.
	// This is CORRECT behavior for 1/sec data tickers.
	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))

	chart := &dirtyNonBoundary{}
	chart.SetVisible(true)
	chart.SetBounds(geometry.NewRect(0, 400, 800, 150))
	chart.SetNeedsRedraw(true)

	root.kids = []widget.Widget{chart}

	if !widget.NeedsRedrawInTreeNonBoundary(root) {
		t.Error("dirty non-boundary chart should be found by NeedsRedrawInTreeNonBoundary")
	}
}

func TestBoundaryDirty_NotFoundByNonBoundaryCheck(t *testing.T) {
	// A dirty RepaintBoundary (spinner) should NOT trigger root re-record
	// via NeedsRedrawInTreeNonBoundary. Boundaries manage their own state.
	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))

	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 48, 48))
	spinner.SetNeedsRedraw(true)

	root.kids = []widget.Widget{spinner}

	if widget.NeedsRedrawInTreeNonBoundary(root) {
		t.Error("dirty boundary (spinner) should NOT be found by NeedsRedrawInTreeNonBoundary — " +
			"boundaries manage their own state independently")
	}
}

// --- Scroll into view re-recording test ---

func TestPaintBoundaryLayers_ReRecordsWhenScrolledIntoView(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 700, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 700))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})

	// Frame 1: offscreen → skipped.
	PaintBoundaryLayersWithContext(root, nil, ctx)
	if spinner.drawCount != 0 {
		t.Fatal("frame 1: offscreen spinner should not draw")
	}

	// Simulate scroll: spinner now inside viewport.
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))

	// Frame 2: visible → should record (scene was kept dirty).
	PaintBoundaryLayersWithContext(root, nil, ctx)
	if spinner.drawCount == 0 {
		t.Error("frame 2: spinner scrolled into view should be recorded (scene was kept dirty)")
	}
	if spinner.IsSceneDirty() {
		t.Error("frame 2: spinner should be clean after recording")
	}
}

// --- Render loop pipeline integration tests ---

// TestMultiFrameSpinnerLifecycle simulates 5 consecutive frames of a visible
// spinner animation and verifies per-frame Draw and ScheduleAnimationFrame
// counts. Each frame should produce exactly 1 Draw call and 1
// ScheduleAnimationFrame call. After each frame, the scene should be clean
// until the spinner re-dirties itself for the next frame.
func TestMultiFrameSpinnerLifecycle(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	animFrameCount := 0
	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})
	ctx.SetOnScheduleAnimation(func() { animFrameCount++ })

	const totalFrames = 5
	for frame := 1; frame <= totalFrames; frame++ {
		prevDraw := spinner.drawCount
		prevSched := spinner.scheduleAnimationCalls

		PaintBoundaryLayersWithContext(root, nil, ctx)

		drawThisFrame := spinner.drawCount - prevDraw
		schedThisFrame := spinner.scheduleAnimationCalls - prevSched

		if drawThisFrame != 1 {
			t.Errorf("frame %d: want 1 Draw call, got %d", frame, drawThisFrame)
		}
		if schedThisFrame != 1 {
			t.Errorf("frame %d: want 1 ScheduleAnimationFrame call, got %d",
				frame, schedThisFrame)
		}
		if spinner.IsSceneDirty() {
			t.Errorf("frame %d: scene should be clean immediately after recording", frame)
		}
		if spinner.CachedScene() == nil {
			t.Errorf("frame %d: spinner should have a cached scene", frame)
		}

		// Simulate the animation pumper re-dirtying the boundary for the
		// next frame (SetNeedsRedraw triggers InvalidateScene on boundaries).
		spinner.InvalidateScene()
	}

	// After 5 frames the totals should match.
	if spinner.drawCount != totalFrames {
		t.Errorf("total draw calls: want %d, got %d", totalFrames, spinner.drawCount)
	}
	if spinner.scheduleAnimationCalls != totalFrames {
		t.Errorf("total ScheduleAnimationFrame calls: want %d, got %d",
			totalFrames, spinner.scheduleAnimationCalls)
	}
	if animFrameCount != totalFrames {
		t.Errorf("total ctx.ScheduleAnimationFrame callbacks: want %d, got %d",
			totalFrames, animFrameCount)
	}
}

// TestDataTickerDoesNotTriggerOffscreenSpinnerRecording verifies the
// interaction between a non-boundary dirty widget (chart receiving data ticks)
// and an offscreen boundary (spinner below viewport). The chart should be
// detected by NeedsRedrawInTreeNonBoundary (causing root re-record), but the
// offscreen spinner must NOT be drawn despite the tree being dirty.
func TestDataTickerDoesNotTriggerOffscreenSpinnerRecording(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	// Chart: non-boundary, dirty from a data tick.
	chart := &dirtyNonBoundary{}
	chart.SetVisible(true)
	chart.SetBounds(geometry.NewRect(0, 400, 800, 150))
	chart.SetNeedsRedraw(true)

	// Spinner: boundary, offscreen below viewport.
	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 800, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 800))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{chart, spinner}

	// NeedsRedrawInTreeNonBoundary should find chart (non-boundary dirty).
	if !widget.NeedsRedrawInTreeNonBoundary(root) {
		t.Fatal("dirty chart (non-boundary) should be detected by NeedsRedrawInTreeNonBoundary")
	}

	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})

	// Record boundaries. Root re-records (chart is part of root subtree),
	// but spinner should be skipped (offscreen).
	PaintBoundaryLayersWithContext(root, nil, ctx)

	if spinner.drawCount != 0 {
		t.Errorf("offscreen spinner should not draw when chart triggers root re-record, "+
			"got %d Draw calls", spinner.drawCount)
	}
	if !spinner.IsSceneDirty() {
		t.Error("offscreen spinner should remain dirty for future scroll-into-view")
	}

	// After root recording, ClearRedrawInTree clears the non-boundary chart.
	// recordBoundary already calls ClearRedrawInTree on the root subtree.
	if chart.NeedsRedraw() {
		// Chart is part of root boundary subtree — recording clears it.
		t.Log("note: chart needsRedraw cleared by root boundary recording (expected)")
	}
}

// TestBoundaryRecordingOrder_RootBeforeChildren verifies depth-first recording
// order: root boundary is recorded first, which stamps CompositorClip on child
// boundaries via DrawChild. Only then are children evaluated for visibility.
func TestBoundaryRecordingOrder_RootBeforeChildren(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	// Both root and spinner are dirty.
	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))
	root.InvalidateScene()

	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})

	PaintBoundaryLayersWithContext(root, nil, ctx)

	// Root should be recorded (depth-first: root runs first).
	if root.CachedScene() == nil {
		t.Error("root boundary should be recorded first")
	}
	// Spinner should be recorded after root (visible, dirty).
	if spinner.CachedScene() == nil {
		t.Error("spinner should be recorded after root establishes CompositorClip")
	}
	if spinner.drawCount == 0 {
		t.Error("spinner Draw should be called during recording")
	}

	// Both should be clean after the paint pass.
	if root.IsSceneDirty() {
		t.Error("root should be clean after recording")
	}
	if spinner.IsSceneDirty() {
		t.Error("spinner should be clean after recording")
	}
}

// TestScreenBoundsAccuracyAfterRecording verifies that ScreenBounds returns
// correct screen-space coordinates for boundaries after PaintBoundaryLayers.
// The onBoundaryDirty callback should use these coordinates for damage rects.
func TestScreenBoundsAccuracyAfterRecording(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(200, 300, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(200, 300))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	var damageRects []geometry.Rect
	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(r geometry.Rect) {
		damageRects = append(damageRects, r)
	})

	// Record to wire onBoundaryDirty callbacks.
	PaintBoundaryLayersWithContext(root, nil, ctx)

	// Verify ScreenBounds for the spinner: origin (200,300), size 48x48.
	spinnerScreen := spinner.ScreenBounds()
	wantSpinnerMin := geometry.Pt(200, 300)
	wantSpinnerMax := geometry.Pt(248, 348)
	if spinnerScreen.Min != wantSpinnerMin || spinnerScreen.Max != wantSpinnerMax {
		t.Errorf("spinner ScreenBounds = %v, want Min=%v Max=%v",
			spinnerScreen, wantSpinnerMin, wantSpinnerMax)
	}

	// Verify ScreenBounds for the root: origin (0,0), size 800x600.
	rootScreen := root.ScreenBounds()
	wantRootMin := geometry.Pt(0, 0)
	wantRootMax := geometry.Pt(800, 600)
	if rootScreen.Min != wantRootMin || rootScreen.Max != wantRootMax {
		t.Errorf("root ScreenBounds = %v, want Min=%v Max=%v",
			rootScreen, wantRootMin, wantRootMax)
	}

	// Invalidate spinner and verify the damage rect matches ScreenBounds.
	spinner.InvalidateScene()

	if len(damageRects) == 0 {
		t.Fatal("expected damage rect from onBoundaryDirty callback")
	}
	dr := damageRects[0]
	if dr.Min != wantSpinnerMin || dr.Max != wantSpinnerMax {
		t.Errorf("damage rect = %v, want Min=%v Max=%v matching ScreenBounds",
			dr, wantSpinnerMin, wantSpinnerMax)
	}
}

// TestCleanStateEarlyReturn validates the frame skip condition: when no
// boundary is dirty and no widget has needsRedraw, the draw pass would
// return early (no GPU work). This tests the prerequisite checks.
func TestCleanStateEarlyReturn(t *testing.T) {
	cleanup := setupSceneRecorder(t)
	defer cleanup()

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	spinner := &animatedBoundary{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 48, 48))
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	spinner.SetCompositorClip(geometry.NewRect(0, 0, 800, 600))
	spinner.InvalidateScene()

	root.kids = []widget.Widget{spinner}

	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})

	// First frame: record everything.
	PaintBoundaryLayersWithContext(root, nil, ctx)

	// After recording, all boundaries should be clean.
	if root.IsSceneDirty() {
		t.Error("root should be clean after recording")
	}
	if spinner.IsSceneDirty() {
		t.Error("spinner should be clean after recording")
	}

	// Clear the redraw flags to simulate frame completion.
	widget.ClearRedrawInTree(root)

	// Now validate all early return conditions.
	if widget.NeedsRedrawInTree(root) {
		t.Error("NeedsRedrawInTree should be false after ClearRedrawInTree — frame skip valid")
	}
	if widget.NeedsRedrawInTreeNonBoundary(root) {
		t.Error("NeedsRedrawInTreeNonBoundary should be false — no dirty non-boundaries")
	}
	if root.IsSceneDirty() {
		t.Error("root scene should remain clean — no re-dirtying occurred")
	}
	if spinner.IsSceneDirty() {
		t.Error("spinner scene should remain clean — no re-dirtying occurred")
	}

	// A second PaintBoundaryLayers pass should not call Draw on the spinner.
	prevDraw := spinner.drawCount
	PaintBoundaryLayersWithContext(root, nil, ctx)
	if spinner.drawCount != prevDraw {
		t.Errorf("clean spinner should not be drawn on second pass, "+
			"got %d new Draw calls", spinner.drawCount-prevDraw)
	}
}

// TestVisibilityMatrix tests all boundary visibility combinations against a
// viewport clip using table-driven subtests. Each case positions a boundary
// at different screen locations relative to the viewport and verifies
// isBoundaryVisible returns the correct result.
func TestVisibilityMatrix(t *testing.T) {
	// Viewport clip: origin (0,0), size 800x600.
	viewport := geometry.NewRect(0, 0, 800, 600)

	tests := []struct {
		name    string
		originX float32
		originY float32
		width   float32
		height  float32
		hasClip bool
		wantVis bool
	}{
		{
			name:    "no clip (root boundary)",
			originX: 0, originY: 0,
			width: 800, height: 600,
			hasClip: false,
			wantVis: true,
		},
		{
			name:    "fully inside viewport",
			originX: 100, originY: 200,
			width: 48, height: 48,
			hasClip: true,
			wantVis: true,
		},
		{
			name:    "outside below viewport",
			originX: 100, originY: 700,
			width: 48, height: 48,
			hasClip: true,
			wantVis: false,
		},
		{
			name:    "outside above viewport",
			originX: 100, originY: -100,
			width: 48, height: 48,
			hasClip: true,
			wantVis: false,
		},
		{
			name:    "outside left of viewport",
			originX: -100, originY: 300,
			width: 48, height: 48,
			hasClip: true,
			wantVis: false,
		},
		{
			name:    "outside right of viewport",
			originX: 900, originY: 300,
			width: 48, height: 48,
			hasClip: true,
			wantVis: false,
		},
		{
			name:    "partially overlapping bottom-right",
			originX: 780, originY: 580,
			width: 48, height: 48,
			hasClip: true,
			wantVis: true,
		},
		{
			name:    "partially overlapping top-left",
			originX: -20, originY: -20,
			width: 48, height: 48,
			hasClip: true,
			wantVis: true,
		},
		{
			name:    "partially overlapping left edge",
			originX: -24, originY: 300,
			width: 48, height: 48,
			hasClip: true,
			wantVis: true,
		},
		{
			name:    "exactly touching right edge (non-intersecting)",
			originX: 800, originY: 300,
			width: 48, height: 48,
			hasClip: true,
			wantVis: false,
		},
		{
			name:    "exactly touching bottom edge (non-intersecting)",
			originX: 100, originY: 600,
			width: 48, height: 48,
			hasClip: true,
			wantVis: false,
		},
		{
			name:    "1px overlap on right edge",
			originX: 799, originY: 300,
			width: 48, height: 48,
			hasClip: true,
			wantVis: true,
		},
		{
			name:    "centered in viewport",
			originX: 376, originY: 276,
			width: 48, height: 48,
			hasClip: true,
			wantVis: true,
		},
		{
			name:    "large boundary fully enclosing viewport",
			originX: -100, originY: -100,
			width: 1000, height: 800,
			hasClip: true,
			wantVis: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &testLeaf{}
			b.SetVisible(true)
			b.SetRepaintBoundary(true)
			b.SetBounds(geometry.NewRect(tt.originX, tt.originY, tt.width, tt.height))
			b.SetScreenOrigin(geometry.Pt(tt.originX, tt.originY))
			if tt.hasClip {
				b.SetCompositorClip(viewport)
			}

			got := isBoundaryVisible(b)
			if got != tt.wantVis {
				t.Errorf("isBoundaryVisible() = %v, want %v "+
					"(origin=(%g,%g), size=%gx%g, viewport=%v)",
					got, tt.wantVis, tt.originX, tt.originY,
					tt.width, tt.height, viewport)
			}
		})
	}
}
