package app

import (
	"github.com/gogpu/gg/scene"
	"github.com/gogpu/ui/compositor"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/widget"
)

// boundaryInfo describes a widget that is a RepaintBoundary.
type boundaryInfo interface {
	widget.Widget
	IsRepaintBoundary() bool
	IsSceneDirty() bool
	CachedScene() *scene.Scene
	SetCachedScene(*scene.Scene)
	ClearSceneDirty()
	SceneCacheSize() (int, int)
	SetSceneCacheSize(int, int)
	Bounds() geometry.Rect
	ScreenOrigin() geometry.Point
}

// BuildLayerTree walks the widget tree and constructs a compositor layer tree.
// Each RepaintBoundary widget produces a PictureLayer inside an OffsetLayer.
// Non-boundary widgets are skipped (they're drawn inside their parent boundary).
//
// NOT IN PRODUCTION PIPELINE: the production render loop (desktop.draw)
// uses PaintBoundaryLayers + renderBoundaryTextures + compositeTextures
// instead. BuildLayerTree is retained for future use with the compositor
// package (animated transforms, opacity layers).
//
// See: ADR-007 Phase 5 (bypassed in favor of Phase 7 per-boundary GPU textures)
// Task: TASK-UI-OPT-005-compositor-integration (backlog)
//
// Flutter equivalent: Layer tree is built during paint via paintChild.
func BuildLayerTree(root widget.Widget) *compositor.OffsetLayerImpl {
	if root == nil {
		return compositor.NewOffsetLayer(geometry.Point{})
	}

	rootLayer := compositor.NewOffsetLayer(geometry.Point{})
	buildLayerRecursive(root, rootLayer, 0, 0)
	return rootLayer
}

// buildLayerRecursive walks the widget tree, adding PictureLayer for each boundary.
// localX/localY accumulate offsets from non-boundary ancestors, so each
// boundary's OffsetLayer gets the correct position relative to its
// parent boundary (not just its immediate parent widget).
func buildLayerRecursive(w widget.Widget, parentLayer compositor.ContainerLayer, localX, localY float32) {
	if w == nil {
		return
	}

	type boundsGetter interface{ Bounds() geometry.Rect }
	var boundsMin geometry.Point
	if bg, ok := w.(boundsGetter); ok {
		boundsMin = bg.Bounds().Min
	}

	bi, isBoundary := w.(boundaryInfo)
	if isBoundary && bi.IsRepaintBoundary() {
		// Offset relative to parent boundary = accumulated local offset + own bounds.Min
		offset := geometry.Pt(localX+boundsMin.X, localY+boundsMin.Y)

		childOffset := compositor.NewOffsetLayer(offset)
		pic := compositor.NewPictureLayer()

		cachedScene := bi.CachedScene()
		if cachedScene != nil {
			pic.SetPicture(cachedScene)
		}
		if bi.IsSceneDirty() {
			pic.MarkDirty()
		} else {
			pic.ClearDirty()
		}

		childOffset.Append(pic)
		parentLayer.Append(childOffset)

		// Recurse into children. Local offset resets to (0,0) because
		// this boundary's OffsetLayer already accounts for its position.
		for _, child := range w.Children() {
			buildLayerRecursive(child, childOffset, 0, 0)
		}
		return
	}

	// Non-boundary widget: accumulate its bounds.Min and recurse.
	nextX := localX + boundsMin.X
	nextY := localY + boundsMin.Y
	for _, child := range w.Children() {
		buildLayerRecursive(child, parentLayer, nextX, nextY)
	}
}

// PaintBoundaryLayers walks the widget tree and re-records dirty boundaries.
// This is the Flutter flushPaint equivalent: only dirty boundary PictureLayers
// are re-recorded. Clean boundaries keep their cached scenes.
//
// After this function, all boundary CachedScene values are fresh.
// The compositor can then Compose the layer tree to assemble the final scene.
// PaintBoundaryLayers re-records dirty boundaries with nil context.
func PaintBoundaryLayers(root widget.Widget, _ *compositor.OffsetLayerImpl) {
	PaintBoundaryLayersWithContext(root, nil, nil)
}

// PaintBoundaryLayersWithContext re-records dirty boundaries with a given context.
func PaintBoundaryLayersWithContext(root widget.Widget, _ *compositor.OffsetLayerImpl, ctx widget.Context) {
	if root == nil {
		return
	}
	paintBoundaryRecursiveCtx(root, ctx)
}

// paintBoundaryRecursiveCtx walks the widget tree, re-recording dirty boundaries.
func paintBoundaryRecursiveCtx(w widget.Widget, ctx widget.Context) {
	paintBoundaryWithDepth(w, ctx, 0)
}

func paintBoundaryWithDepth(w widget.Widget, ctx widget.Context, _ int) {
	if w == nil {
		return
	}

	bi, isBoundary := w.(boundaryInfo)
	if isBoundary && bi.IsRepaintBoundary() {
		// Record only if dirty AND visible. Offscreen boundaries (outside
		// CompositorClip viewport) are skipped: Draw never runs →
		// ScheduleAnimationFrame never called → animation pumper stops.
		// Scene stays dirty so it re-records when scrolled back into view.
		if (bi.IsSceneDirty() || bi.CachedScene() == nil) && isBoundaryVisible(bi) {
			recordBoundary(bi, ctx)
		}

		for _, child := range w.Children() {
			paintBoundaryWithDepth(child, ctx, 0)
		}
		return
	}

	for _, child := range w.Children() {
		paintBoundaryWithDepth(child, ctx, 0)
	}
}

// recordBoundary re-records a boundary widget's scene via SceneCanvas.
func recordBoundary(bi boundaryInfo, ctx widget.Context) {
	// Wire onBoundaryDirty callback so animated widgets (spinner) that call
	// SetNeedsRedraw during Draw trigger RequestRedraw for the next frame.
	// Without this, InvalidateScene sets sceneDirty but nobody wakes the
	// render loop → animation frozen at data ticker rate (1fps).
	type callbackSetter interface {
		SetOnBoundaryDirty(func())
	}
	if cs, ok := bi.(callbackSetter); ok && ctx != nil {
		capturedBi := bi
		cs.SetOnBoundaryDirty(func() {
			bounds := capturedBi.Bounds()
			origin := capturedBi.ScreenOrigin()
			ctx.InvalidateRect(geometry.Rect{
				Min: origin,
				Max: geometry.Pt(origin.X+bounds.Width(), origin.Y+bounds.Height()),
			})
		})
	}
	bounds := bi.Bounds()
	width := int(bounds.Width())
	height := int(bounds.Height())

	if width <= 0 || height <= 0 {
		return
	}

	// Check size change.
	cw, ch := bi.SceneCacheSize()
	if cw != width || ch != height {
		bi.SetSceneCacheSize(width, height)
	}

	cachedScene := bi.CachedScene()
	if cachedScene == nil {
		cachedScene = scene.NewScene()
	}
	cachedScene.Reset()

	if widget.GetSceneRecorderFactory() == nil {
		return
	}

	recorder, cleanup := widget.GetSceneRecorderFactory()(cachedScene, width, height)

	// Propagate device scale for HiDPI-aware SVG icon rasterization (ADR-026).
	if ctx != nil {
		if ds, ok := recorder.(widget.DeviceScaler); ok {
			ds.SetDeviceScale(ctx.Scale())
		}
	}

	// Clear dirty BEFORE Draw (Flutter pattern: detect re-dirtying during Draw).
	bi.ClearSceneDirty()
	widget.ClearRedrawInTree(bi)

	// Suppress boundary dirty callback during recording. Animated widgets
	// (spinner) call SetNeedsRedraw inside Draw which triggers InvalidateScene.
	// Without suppression, this fires onBoundaryDirty → ctx.InvalidateRect →
	// immediate RequestRedraw → 60fps forced. With suppression, the widget
	// uses ScheduleAnimationFrame for deferred render at animPumper rate.
	// External events (hover, click) fire OUTSIDE Draw → not suppressed.
	type dirtySuppressor interface{ SetSuppressDirtyCallback(bool) }
	if ds, ok := bi.(dirtySuppressor); ok {
		ds.SetSuppressDirtyCallback(true)
	}

	// Set ScreenOriginBase so StampScreenOrigin inside Draw computes correct
	// screen-space origins for child widgets (Flutter PaintingContext.offset).
	// Without this, nested boundaries (ScrollView → items) get ScreenOrigin
	// relative to (0,0) instead of the boundary's actual screen position.
	//
	// ScreenOriginBase must compensate for the PushTransform(-bounds.Min) below.
	// After PushTransform, TransformOffset = -bounds.Min. StampScreenOrigin
	// computes: offset = TransformOffset + ScreenOriginBase = -bounds.Min + base.
	// For a child at childBounds.Min, screenOrigin = offset + childBounds.Min.
	// We want: screenOrigin = bi.ScreenOrigin() + childBounds.Min.
	// So: base = bi.ScreenOrigin() + bounds.Min.
	type screenBaseSetter interface{ SetScreenOriginBase(geometry.Point) }
	if sbs, ok := recorder.(screenBaseSetter); ok {
		sbs.SetScreenOriginBase(bi.ScreenOrigin().Add(bounds.Min))
	}

	// Record in local coordinates.
	recorder.PushTransform(geometry.Pt(-bounds.Min.X, -bounds.Min.Y))
	bi.Draw(ctx, recorder)
	recorder.PopTransform()

	if ds, ok := bi.(dirtySuppressor); ok {
		ds.SetSuppressDirtyCallback(false)
	}
	cleanup()

	bi.SetCachedScene(cachedScene)
}

// isBoundaryVisible checks whether a boundary widget is inside its compositor
// clip rect (viewport). Boundaries without a clip (root, non-scrolled) are
// always visible. Only boundaries with CompositorClip set by DrawChild during
// parent recording can be culled.
//
// See: ADR-007 Phase 7 (per-boundary GPU textures, offscreen culling)
// Task: TASK-UI-ADR007-PHASE7 (done)
func isBoundaryVisible(bi boundaryInfo) bool {
	type clipChecker interface {
		HasCompositorClip() bool
		CompositorClip() geometry.Rect
	}
	cc, ok := bi.(clipChecker)
	if !ok || !cc.HasCompositorClip() {
		return true
	}
	clip := cc.CompositorClip()
	origin := bi.ScreenOrigin()
	bounds := bi.Bounds()
	screenRect := geometry.Rect{
		Min: origin,
		Max: geometry.Pt(origin.X+bounds.Width(), origin.Y+bounds.Height()),
	}
	return screenRect.Intersects(clip)
}
