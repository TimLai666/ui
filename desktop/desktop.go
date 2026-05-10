package desktop

import (
	"fmt"
	"image"
	"log"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/integration/ggcanvas"
	"github.com/gogpu/gg/scene"
	"github.com/gogpu/gogpu"
	"github.com/gogpu/gpucontext"
	"github.com/gogpu/ui/app"
	"github.com/gogpu/ui/compositor"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/render"
	"github.com/gogpu/ui/widget"
)

// Run starts a desktop application with a scene-composition render loop.
//
// ADR-007 Phase 4-5: retained-mode compositor with display list caching.
//
// The rendering pipeline:
//  1. Frame: flush signals, layout, animations (Window.Frame)
//  2. Draw: full DrawTree into render.Canvas (gg.Context GPU pipeline)
//     - RepaintBoundary cache hit: ReplayScene replays cached scene.Scene
//     - RepaintBoundary cache miss: re-record child.Draw into scene
//  3. Present: FlushGPUWithView sends all GPU shapes to surface in one pass
//
// No retained CPU pixmap. No RasterizerAnalytic hack. No drawDirtyRegions.
// GPU SDF shapes are re-queued every frame via scene replay.
//
// Run blocks until the window is closed.
func Run(gogpuApp *gogpu.App, uiApp *app.App) error {
	if gogpuApp == nil {
		return fmt.Errorf("desktop: gogpuApp must not be nil")
	}
	if uiApp == nil {
		return fmt.Errorf("desktop: uiApp must not be nil")
	}

	// Scene composition: full draw every frame, RepaintBoundary cache for efficiency.
	uiApp.Window().SetRenderMode(app.RenderModeHostManaged)

	rl := &renderLoop{
		gogpuApp: gogpuApp,
		uiApp:    uiApp,
	}

	gogpuApp.OnDraw(rl.draw)

	gogpuApp.OnClose(func() {
		rl.releaseBoundaryTextures()
		gg.CloseAccelerator()
		if rl.canvas != nil {
			_ = rl.canvas.Close()
		}
	})

	return gogpuApp.Run()
}

// renderLoop holds the state for the scene-composition render loop.
//
// ADR-007 Phase 7: per-boundary GPU textures. Each RepaintBoundary owns an
// offscreen GPU texture. Dirty boundaries re-render into their texture.
// Clean boundaries reuse previous texture (0 GPU work). Compositor blits
// all textures via non-MSAA path instead of replaying all scenes through
// MSAA SDF pipeline.
type renderLoop struct {
	gogpuApp     *gogpu.App
	uiApp        *app.App
	canvas       *ggcanvas.Canvas
	debugOverlay dirtyOverlay

	// Per-boundary GPU texture cache. Key = boundary cache key (uint64).
	// Each boundary rendered into its own offscreen texture.
	// Clean boundaries: texture reused. Dirty: re-rendered.
	boundaryTextures map[uint64]*boundaryTexEntry
	fullRedrawNeeded bool // First frame, resize, theme change
}

// boundaryTexEntry holds an offscreen GPU texture for a RepaintBoundary.
type boundaryTexEntry struct {
	texture      gpucontext.TextureView
	release      func()
	width        int
	height       int
	sceneVersion uint64        // tracks which scene version was last rendered into texture
	clipRect     geometry.Rect // screen-space clip for compositor scissoring
	hasClip      bool          // whether clipRect is set
}

// draw is the OnDraw callback registered with gogpu.App.
//
// ADR-007 Phase 4-5: full-tree draw with RepaintBoundary display list cache.
//
// Every frame:
//  1. Frame() flushes signals, layouts, animations
//  2. Full DrawTree into render.Canvas (gg.Context GPU pipeline)
//     - RepaintBoundary cache hit: ReplayScene → GPU shapes from cached scene
//     - RepaintBoundary cache miss: re-record child.Draw → scene, then replay
//  3. FlushGPUWithView presents all GPU shapes in single render pass
//
// No persistent pixmap. No partial redraw. No RasterizerAnalytic hack.
// GPU SDF shapes are re-queued every frame via scene replay — no ephemeral
// shape loss. RepaintBoundary cache ensures O(dirty) re-recording cost.
func (rl *renderLoop) draw(dc *gogpu.Context) {
	w, h := dc.Width(), dc.Height()
	if w <= 0 || h <= 0 {
		return
	}

	if rl.canvas == nil {
		if !rl.initCanvas(w, h) {
			return
		}
	}

	rl.uiApp.Frame()

	cw, ch := rl.canvas.Size()
	if cw != w || ch != h {
		if err := rl.canvas.Resize(w, h); err != nil {
			log.Printf("desktop: canvas.Resize: %v", err)
		}
		cw, ch = w, h
		rl.releaseBoundaryTextures()
		rl.fullRedrawNeeded = true
	}

	win := rl.uiApp.Window()

	// ADR-007 D2: skip GPU work when nothing changed. Frame() already ran
	// (signals, layout, animations). If no boundary is dirty and no widget
	// needs redraw, the previous frame's GPU output is still valid — reuse it.
	// This is the retained-mode "0% GPU on idle" optimization.
	//
	// See: ADR-007 Phase 7, TASK-UI-OPT-001 (done: frame skip)
	// Next: TASK-UI-OPT-003 (LoadOpLoad for <3% spinner GPU)
	if !rl.fullRedrawNeeded && !win.HasDirtyBoundariesOrNeedsRedraw() &&
		!widget.NeedsRedrawInTree(win.Root()) {
		return
	}

	cc := rl.canvas.Context()

	gg.BeginAcceleratorFrame()
	cc.BeginGPUFrame()
	cc.ResetFrameDamage()

	// ADR-007 Phase 7: Per-boundary GPU textures.
	//
	// Each RepaintBoundary rendered into its own offscreen GPU texture.
	// Dirty boundaries: re-render scene into texture (MSAA, LoadOpClear).
	// Clean boundaries: reuse previous texture (0 GPU work).
	// Compositor: blit all textures via non-MSAA path.
	//
	// Enterprise references: Flutter RasterCache, Chrome TileManager.
	// Research: docs/dev/research/PER-BOUNDARY-GPU-TEXTURES-RESEARCH.md
	root := win.Root()
	winCtx := win.Context()

	// Root boundary is always at window origin (0,0).
	type originSetter interface{ SetScreenOrigin(geometry.Point) }
	if os, ok := root.(originSetter); ok {
		os.SetScreenOrigin(geometry.Point{})
	}

	// If any NON-BOUNDARY widget needs redraw (e.g., ScrollView after
	// setScroll without parent chain), force root re-recording.
	// Boundary widgets manage their own dirty state — they don't need
	// to trigger root re-recording. This prevents offscreen animated
	// boundaries (spinner) from forcing 60fps root re-recording.
	if widget.NeedsRedrawInTreeNonBoundary(root) {
		type sceneDirtier interface {
			IsRepaintBoundary() bool
			InvalidateScene()
		}
		if sd, ok := root.(sceneDirtier); ok && sd.IsRepaintBoundary() {
			// Suppress onBoundaryDirty callback: we're already inside the
			// render loop — no external notification needed. Without this,
			// InvalidateScene fires ctx.InvalidateRect which restarts the
			// animation pumper at 30fps for data tickers that only need 1fps.
			type dirtySuppressor interface{ SetSuppressDirtyCallback(bool) }
			if ds, ok2 := root.(dirtySuppressor); ok2 {
				ds.SetSuppressDirtyCallback(true)
				sd.InvalidateScene()
				ds.SetSuppressDirtyCallback(false)
			} else {
				sd.InvalidateScene()
			}
		}
	}

	app.PaintBoundaryLayersWithContext(root, nil, winCtx)

	// CollectDirtyRegions AFTER PaintBoundaryLayers: root recording stamps
	// fresh ScreenOrigin on child boundaries via StampScreenOrigin/DrawChild.
	// Before this fix, CollectDirtyRegions ran before recording → spinner
	// ScreenOrigin was stale (0,0) → damage rect at top-left corner.
	win.CollectDirtyRegions()

	// Render dirty boundaries into offscreen textures.
	if rl.boundaryTextures == nil {
		rl.boundaryTextures = make(map[uint64]*boundaryTexEntry)
		rl.fullRedrawNeeded = true
	}
	rl.renderBoundaryTextures(root, cc)

	// Compositor: blit all boundary textures onto surface.
	rl.compositeTextures(root, cc, cw, ch)

	// Overlays drawn on top (dropdowns, dialogs).
	widgetCanvas := render.NewCanvas(cc, cw, ch)
	win.DrawOverlays(widgetCanvas)
	win.ClearAfterPaint()
	win.ClearDirtyBoundaries()

	// Debug overlay: cyan flash-and-fade on dirty widget regions (ADR-023).
	if isDebugDirtyEnabled() {
		rl.debugOverlay.update(win.DirtyRegions())
		rl.debugOverlay.draw(cc, rl.canvas.DeviceScale())
		if rl.debugOverlay.needsAnimationFrame() {
			rl.gogpuApp.RequestRedraw()
		}
	}

	// ADR-021 Phase 7: Pass damage rects to gg for partial present.
	// ui knows which boundaries are dirty → their screen bounds = damage rects.
	// Chain: ui → gg SetPresentDamage → gogpu SetDamageRects → wgpu PresentWithDamage → OS.
	if dirtyRegions := win.DirtyRegions(); len(dirtyRegions) > 0 {
		rects := make([]image.Rectangle, len(dirtyRegions))
		for i, r := range dirtyRegions {
			rects[i] = image.Rect(
				int(r.Min.X), int(r.Min.Y),
				int(r.Max.X+0.5), int(r.Max.Y+0.5),
			)
		}
		rl.canvas.SetPresentDamage(rects)
	}

	// Present via canvas.Render — single entry point for ALL backends (ADR-022).
	// GPU direct path used when available, CPU fallback on software adapter.
	// MarkDirty required because desktop.go draws directly to Context
	// (not via canvas.Draw(fn) which sets dirty automatically).
	rl.canvas.MarkDirty()
	if err := rl.canvas.Render(dc.RenderTarget()); err != nil {
		log.Printf("desktop: canvas.Render: %v", err)
	}

	// Request extra frames for gg-level damage overlay fade (GOGPU_DEBUG_DAMAGE=1).
	if rl.canvas.NeedsAnimationFrame() {
		rl.gogpuApp.RequestRedraw()
	}
}

// replayLayerTree walks the layer tree and replays each PictureLayer
// individually with per-layer damage tracking.
//
// Dirty layers replay WITH damage tracking → green overlay shows them.
// Clean layers replay with damage SUPPRESSED → green overlay skips them.
//
// This is the Flutter compositeFrame pattern: addRetained for clean
// layers (no engine work), addToScene for dirty layers (rebuild).
// Our equivalent: SetDamageTracking(false) for clean layers.
func replayLayerTree(layer compositor.Layer, canvas widget.Canvas) {
	if layer == nil {
		return
	}

	offset := layer.Offset()
	hasOffset := offset.X != 0 || offset.Y != 0

	if hasOffset {
		canvas.PushTransform(offset)
	}

	if po, ok := layer.(compositor.PictureOwner); ok {
		pic := po.Picture()
		if pic != nil && !pic.IsEmpty() {
			canvas.ReplayScene(pic)
		}
	}

	if cl, ok := layer.(compositor.ContainerLayer); ok {
		for _, child := range cl.Children() {
			replayLayerTree(child, canvas)
		}
	}

	if hasOffset {
		canvas.PopTransform()
	}
}

// renderBoundaryTextures walks the widget tree and renders dirty RepaintBoundary
// widgets into their own offscreen GPU textures. Clean boundaries keep their
// previous texture (0 GPU work).
//
// This replaces the old replayLayerTree approach which replayed ALL scenes
// through MSAA SDF pipeline every frame. Now only dirty boundaries render
// (into small offscreen textures), clean boundaries are just texture blits.
func (rl *renderLoop) renderBoundaryTextures(w widget.Widget, cc *gg.Context) {
	rl.renderBoundaryTexturesRecursive(w, cc, 0)
}

func (rl *renderLoop) renderBoundaryTexturesRecursive(w widget.Widget, cc *gg.Context, depth int) {
	if w == nil {
		return
	}

	type boundaryInfo interface {
		widget.Widget
		IsRepaintBoundary() bool
		IsSceneDirty() bool
		CachedScene() *scene.Scene
		BoundaryCacheKey() uint64
		Bounds() geometry.Rect
		Parent() widget.Widget
	}

	if bi, ok := w.(boundaryInfo); ok && bi.IsRepaintBoundary() {
		if depth > 1 {
			return
		}

		// Skip non-root boundaries with uninitialized ScreenOrigin.
		if depth > 0 {
			type originValidator interface{ IsScreenOriginValid() bool }
			if ov, ok2 := w.(originValidator); ok2 && !ov.IsScreenOriginValid() {
				return
			}
		}

		// Skip rendering textures for items outside parent viewport.
		if depth > 0 {
			type compositorClipper interface {
				HasCompositorClip() bool
				CompositorClip() geometry.Rect
				ScreenOrigin() geometry.Point
			}
			if cc2, ok2 := w.(compositorClipper); ok2 && cc2.HasCompositorClip() {
				clip := cc2.CompositorClip()
				origin := cc2.ScreenOrigin()
				bounds := bi.Bounds()
				screenRect := geometry.Rect{
					Min: origin,
					Max: geometry.Pt(origin.X+bounds.Width(), origin.Y+bounds.Height()),
				}
				if !screenRect.Intersects(clip) {
					return
				}
			}
		}

		rl.renderSingleBoundary(bi, cc)

		// Store clip rect in texture entry for compositor scissoring.
		if depth > 0 {
			type compositorClipper interface {
				HasCompositorClip() bool
				CompositorClip() geometry.Rect
			}
			if cc2, ok2 := w.(compositorClipper); ok2 && cc2.HasCompositorClip() {
				key := bi.BoundaryCacheKey()
				if entry := rl.boundaryTextures[key]; entry != nil {
					entry.clipRect = cc2.CompositorClip()
					entry.hasClip = true
				}
			}
		}

		for _, child := range w.Children() {
			rl.renderBoundaryTexturesRecursive(child, cc, depth+1)
		}
		return
	}

	for _, child := range w.Children() {
		rl.renderBoundaryTexturesRecursive(child, cc, depth)
	}
}

// compositeTextures blits all boundary textures onto the surface.
// Root boundary = DrawGPUTextureBase (background), others = DrawGPUTexture (overlays).
// This uses the non-MSAA blit-only path (encodeBlitOnlyPass) — no MSAA overhead.
//
// See: ADR-007 Phase 7 (per-boundary GPU textures)
// Task: TASK-UI-ADR007-PHASE7 (done)
// Next: TASK-UI-OPT-003 (LoadOpLoad + damage rect scissor for <3% GPU)
func (rl *renderLoop) compositeTextures(w widget.Widget, cc *gg.Context, _, _ int) {
	isFirst := true
	rl.walkBoundaries(w, func(key uint64, screenPos geometry.Point, bw, bh int) {
		entry := rl.boundaryTextures[key]
		if entry == nil || entry.texture.IsNil() {
			return
		}

		// Use ScreenOrigin (window-space) for positioning, NOT Bounds().Min (local).
		// ListView items have Bounds (0, y) in content-space but ScreenOrigin
		// reflects accumulated transforms from parent Draw passes.
		x, y := float64(screenPos.X), float64(screenPos.Y)

		if isFirst {
			cc.DrawGPUTextureBase(entry.texture, x, y, bw, bh)
			isFirst = false
		} else if entry.hasClip {
			clip := entry.clipRect
			cc.Push()
			cc.ClipRect(float64(clip.Min.X), float64(clip.Min.Y),
				float64(clip.Width()), float64(clip.Height()))
			cc.DrawGPUTexture(entry.texture, x, y, bw, bh)
			cc.Pop()
		} else {
			cc.DrawGPUTexture(entry.texture, x, y, bw, bh)
		}
	})

	rl.fullRedrawNeeded = false
}

// walkBoundaries walks the widget tree depth-first, calling fn for each RepaintBoundary.
func (rl *renderLoop) walkBoundaries(w widget.Widget, fn func(key uint64, screenPos geometry.Point, width, height int)) {
	rl.walkBoundariesRecursive(w, fn, 0)
}

func (rl *renderLoop) walkBoundariesRecursive(w widget.Widget, fn func(key uint64, screenPos geometry.Point, width, height int), depth int) {
	if w == nil {
		return
	}

	type boundaryChecker interface {
		IsRepaintBoundary() bool
		BoundaryCacheKey() uint64
		Bounds() geometry.Rect
		ScreenOrigin() geometry.Point
	}

	if bi, ok := w.(boundaryChecker); ok && bi.IsRepaintBoundary() {
		if depth > 1 {
			return
		}

		bounds := bi.Bounds()
		screenPos := bi.ScreenOrigin()
		bw, bh := int(bounds.Width()), int(bounds.Height())

		// Skip non-root boundaries that were never drawn (viewport-culled).
		// Their ScreenOrigin is uninitialized (0,0) — compositing would
		// place the texture at the wrong position.
		if depth > 0 {
			type originValidator interface{ IsScreenOriginValid() bool }
			if ov, ok2 := w.(originValidator); ok2 && !ov.IsScreenOriginValid() {
				return
			}
		}

		// Compositor clip (separate concern from boundary checking):
		// skip items fully outside their parent's viewport.
		// Uses interface extension via type assertion — same pattern as
		// Focusable, DeviceScaler, DrawStatsProvider in codebase.
		if depth > 0 {
			type compositorClipper interface {
				HasCompositorClip() bool
				CompositorClip() geometry.Rect
			}
			if cc, ok2 := w.(compositorClipper); ok2 && cc.HasCompositorClip() {
				clip := cc.CompositorClip()
				screenRect := geometry.Rect{
					Min: screenPos,
					Max: geometry.Pt(screenPos.X+float32(bw), screenPos.Y+float32(bh)),
				}
				if !screenRect.Intersects(clip) {
					return
				}
			}
		}

		fn(bi.BoundaryCacheKey(), screenPos, bw, bh)
		for _, child := range w.Children() {
			rl.walkBoundariesRecursive(child, fn, depth+1)
		}
		return
	}

	for _, child := range w.Children() {
		rl.walkBoundariesRecursive(child, fn, depth)
	}
}

// renderSingleBoundary renders one boundary's scene into its offscreen texture.
func (rl *renderLoop) renderSingleBoundary(bi interface {
	widget.Widget
	IsRepaintBoundary() bool
	IsSceneDirty() bool
	CachedScene() *scene.Scene
	BoundaryCacheKey() uint64
	Bounds() geometry.Rect
	Parent() widget.Widget
}, cc *gg.Context) {
	key := bi.BoundaryCacheKey()
	bounds := bi.Bounds()
	bw, bh := int(bounds.Width()), int(bounds.Height())
	if bw <= 0 || bh <= 0 {
		return
	}

	entry := rl.boundaryTextures[key]

	if entry == nil || entry.width != bw || entry.height != bh {
		if entry != nil && entry.release != nil {
			entry.release()
		}
		tex, release := cc.CreateOffscreenTexture(bw, bh)
		entry = &boundaryTexEntry{texture: tex, release: release, width: bw, height: bh}
		rl.boundaryTextures[key] = entry
		rl.fullRedrawNeeded = true
	}

	cachedScene := bi.CachedScene()

	// Check if scene was freshly recorded by PaintBoundaryLayers.
	// PaintBoundaryLayers clears sceneDirty BEFORE recording, so IsSceneDirty()
	// returns false even for just-recorded scenes. Use SceneCacheVersion to detect
	// fresh recordings — version increments on each re-record.
	type versioner interface{ SceneCacheVersion() uint64 }
	currentVersion := uint64(0)
	if v, ok := bi.(versioner); ok {
		currentVersion = v.SceneCacheVersion()
	}
	sceneChanged := entry.sceneVersion != currentVersion

	if !sceneChanged && !bi.IsSceneDirty() && !rl.fullRedrawNeeded && cachedScene != nil {
		return
	}
	if cachedScene == nil || cachedScene.IsEmpty() {
		return
	}

	// Root boundary: draw theme background before scene content.
	if bi.Parent() == nil {
		win := rl.uiApp.Window()
		bg := win.ThemeBackground()
		cc.SetRGBA(float64(bg.R), float64(bg.G), float64(bg.B), float64(bg.A))
		cc.DrawRectangle(0, 0, float64(bw), float64(bh))
		_ = cc.Fill()
	}

	renderer := scene.NewGPUSceneRenderer(cc)
	_ = renderer.RenderScene(cachedScene)
	w, h := uint32(max(bw, 0)), uint32(max(bh, 0)) //nolint:gosec // bw/bh checked > 0 above
	if err := cc.FlushGPUWithView(entry.texture, w, h); err != nil {
		log.Printf("desktop: FlushGPUWithView boundary %d: %v", key, err)
	}
	entry.sceneVersion = currentVersion
}

// releaseBoundaryTextures frees all offscreen GPU textures.
func (rl *renderLoop) releaseBoundaryTextures() {
	for _, entry := range rl.boundaryTextures {
		if entry.release != nil {
			entry.release()
		}
	}
	rl.boundaryTextures = nil
}

// initCanvas creates the ggcanvas lazily on the first draw call.
func (rl *renderLoop) initCanvas(w, h int) bool {
	provider := rl.gogpuApp.GPUContextProvider()
	if provider == nil {
		return false
	}
	var err error
	rl.canvas, err = ggcanvas.New(provider, w, h)
	if err != nil {
		log.Printf("desktop: ggcanvas.New: %v", err)
		return false
	}
	// Set LCD subpixel layout once on the main canvas context.
	// NOT in NewCanvas — calling SetLCDLayout on offscreen contexts
	// triggers GlyphMaskEngine atlas.Clear(), breaking GPU text.
	rl.canvas.Context().SetLCDLayout(gg.LCDLayoutRGB)
	return true
}
