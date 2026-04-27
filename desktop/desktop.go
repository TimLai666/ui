package desktop

import (
	"fmt"
	"image"
	"log"
	"math"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/integration/ggcanvas"
	"github.com/gogpu/gogpu"
	"github.com/gogpu/gpucontext"
	"github.com/gogpu/ui/app"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/render"
)

// Run starts a desktop application with a managed render loop.
//
// This function encapsulates the entire rendering pipeline:
//   - Lazy creation and resizing of ggcanvas
//   - Background drawing (framework-managed)
//   - Widget tree rendering via [app.Window.DrawTo]
//   - Zero-readback GPU presentation (ADR-006)
//   - Resource cleanup on close
//
// Run forces [app.RenderModeFrameworkManaged] to enable the full
// four-level incremental rendering pipeline (ADR-004 + ADR-006):
//   - Level 0: frame skip when nothing changed (zero CPU, zero GPU)
//   - Level 1: dirty-region-only repaint (Qt QBackingStore pattern)
//   - Level 2: partial texture upload (MarkDirtyRegion, 62KB vs 1.92MB)
//   - Level 3: zero-readback GPU rendering (ADR-006 Phase 1)
//
// The rendering pipeline splits CPU and GPU content into two layers:
//   - CPU layer: text, lines, curves rendered to persistent pixmap,
//     partially uploaded to GPU texture, presented as textured quad.
//   - GPU layer: SDF shapes (circles, rounded rects) rendered directly
//     to the swapchain surface via FlushGPUWithView (no readback).
//
// This eliminates the GPU-to-CPU readback that previously cost ~6% GPU
// per frame for any GPU-accelerated shape.
//
// Run blocks until the window is closed and returns any error from the
// underlying [gogpu.App.Run].
//
// The caller is responsible for calling [app.App.SetRoot] before Run.
func Run(gogpuApp *gogpu.App, uiApp *app.App) error {
	if gogpuApp == nil {
		return fmt.Errorf("desktop: gogpuApp must not be nil")
	}
	if uiApp == nil {
		return fmt.Errorf("desktop: uiApp must not be nil")
	}

	// FrameworkManaged: persistent pixmap + dirty-region-only repaint.
	uiApp.Window().SetRenderMode(app.RenderModeFrameworkManaged)

	rl := &renderLoop{
		gogpuApp: gogpuApp,
		uiApp:    uiApp,
	}

	gogpuApp.OnDraw(rl.draw)

	gogpuApp.OnClose(func() {
		gg.CloseAccelerator()
		if rl.canvas != nil {
			_ = rl.canvas.Close()
		}
	})

	return gogpuApp.Run()
}

// swapchainDepth is the number of swapchain buffers that must each receive
// a full LoadOpClear render before damage-aware LoadOpLoad can safely
// preserve their content. Typical swapchain depth is 2-3 buffers.
const swapchainDepth = 3

// renderLoop holds the state for the managed render loop.
// Each desktop.Run call creates exactly one renderLoop.
type renderLoop struct {
	gogpuApp      *gogpu.App
	uiApp         *app.App
	canvas        *ggcanvas.Canvas
	textureReady  bool // true after the first Render promotes pendingTexture
	warmupFrames  int  // full-clear frames remaining for swapchain warmup
	lastDirtyRect image.Rectangle
}

// draw is the OnDraw callback registered with gogpu.App.
//
// gogpu calls OnDraw only when a redraw was requested (event-driven mode)
// or continuously (game loop mode). Either way, the acquired GPU surface
// MUST receive valid content because gogpu presents it unconditionally
// after this callback returns.
//
// ADR-007 Phase 2: On frames where DrawTo returns false (nothing changed),
// we still call present() because gogpu presents unconditionally. However,
// the previous pixmap content is preserved (persistent pixmap pattern) and
// the upload is a no-op when no dirty region is marked.
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

	// Frame: flush signals, layout, animation, cursor sync.
	rl.uiApp.Frame()

	// Resize canvas to match window if needed.
	cw, ch := rl.canvas.Size()
	if cw != w || ch != h {
		if err := rl.canvas.Resize(w, h); err != nil {
			log.Printf("desktop: canvas.Resize: %v", err)
		}
		cw, ch = w, h
		// Resize destroys the GPU texture; next FlushPixmap creates a new
		// pendingTexture that must be promoted via Render on the next frame.
		rl.textureReady = false
		rl.warmupFrames = swapchainDepth
	}

	// ADR-007: Retained-mode compositor.
	//
	// All widget drawing goes to the persistent CPU pixmap via gg.Context
	// with CPU rasterization forced (RasterizerAnalytic). This ensures ALL
	// shapes (including rounded rects, shadows) land in the pixmap — not
	// in the ephemeral GPU SDF queue that gets cleared every frame.
	//
	// The GPU is used ONLY for final compositing: the pixmap is uploaded
	// as a textured quad and presented via FlushGPUWithView. This is the
	// Chrome/Flutter pattern: paint → raster (CPU) → composite (GPU).
	//
	// RepaintBoundary caches child drawing as scene.Scene display lists.
	// On cache hit, ReplayScene replays into the gg.Context (CPU raster).
	// On cache miss, child.Draw re-records into SceneCanvas.
	win := rl.uiApp.Window()
	cc := rl.canvas.Context()

	// Force CPU rasterization so all shapes go to the persistent pixmap.
	// GPU SDF shapes are ephemeral (cleared by BeginGPUFrame) and cause
	// flickering when partial redraws skip parts of the widget tree.
	savedMode := cc.RasterizerMode()
	cc.SetRasterizerMode(gg.RasterizerAnalytic)

	widgetCanvas := render.NewCanvas(cc, cw, ch)
	drawn := win.DrawTo(widgetCanvas)

	cc.SetRasterizerMode(savedMode)

	win.PaintDirtyBoundaries()

	rl.lastDirtyRect = image.Rectangle{}
	if drawn {
		if win.WasFullRepaint() {
			rl.canvas.MarkDirty()
		} else {
			du := win.LastDirtyUnion()
			dr := dirtyUnionToPixelRect(du)
			rl.canvas.MarkDirtyRegion(dr)
			rl.lastDirtyRect = dr
		}
	}

	// GPU compositing: upload pixmap + present as textured quad.
	gg.BeginAcceleratorFrame()
	cc.BeginGPUFrame()
	rl.present(dc)
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

// present composites the CPU pixmap onto the GPU swapchain surface.
//
// Uses damage-aware rendering: after all swapchain buffers have been
// initialized with full LoadOpClear frames (warmup), partial repaints
// pass a damage rect to FlushGPUWithViewDamage which uses LoadOpLoad +
// scissor — only damaged pixels are composited, rest preserved from the
// previous frame in that swapchain buffer.
func (rl *renderLoop) present(dc *gogpu.Context) {
	rt := dc.RenderTarget()

	// First frame or after resize: the GPU texture does not exist yet.
	if !rl.textureReady {
		if err := rl.canvas.Render(&promotionTarget{rt: rt}); err != nil {
			log.Printf("desktop: Render (texture promotion): %v", err)
			return
		}
		rl.textureReady = true
		rl.warmupFrames = swapchainDepth
		return
	}

	// Zero-readback path: upload CPU pixmap without GPU readback.
	tex, err := rl.canvas.FlushPixmap()
	if err != nil {
		log.Printf("desktop: FlushPixmap: %v", err)
		return
	}

	// Get the pixmap texture's GPU view for compositing.
	gt, ok := tex.(*gogpu.Texture)
	if !ok {
		if err := rt.PresentTexture(tex); err != nil {
			log.Printf("desktop: PresentTexture fallback: %v", err)
		}
		return
	}

	cc := rl.canvas.Context()
	cw, ch := rl.canvas.Size()
	cc.DrawGPUTextureBase(gt.TextureView(), 0, 0, cw, ch)

	sv := dc.RenderTarget().SurfaceView()
	if sv.IsNil() {
		return
	}
	sw, sh := dc.SurfaceSize()

	damage := rl.lastDirtyRect
	if rl.warmupFrames > 0 {
		rl.warmupFrames--
		damage = image.Rectangle{}
	}
	if err := cc.FlushGPUWithViewDamage(sv, sw, sh, damage); err != nil {
		log.Printf("desktop: FlushGPUWithViewDamage: %v", err)
	}
}

// promotionTarget wraps RenderTarget with nil SurfaceView to force
// canvas.Render through the Flush → promoteIfPending path on the first frame.
// Without this, Render takes the RenderDirect path (GPU-direct) when
// SurfaceView is non-nil, which bypasses texture creation entirely.
type promotionTarget struct{ rt ggcanvas.RenderTarget }

func (t *promotionTarget) SurfaceView() gpucontext.TextureView { return gpucontext.TextureView{} }
func (t *promotionTarget) SurfaceSize() (uint32, uint32)       { return t.rt.SurfaceSize() }
func (t *promotionTarget) PresentTexture(tex any) error        { return t.rt.PresentTexture(tex) }

func (t *promotionTarget) TextureCreator() gpucontext.TextureCreator {
	type tc interface {
		TextureCreator() gpucontext.TextureCreator
	}
	if p, ok := t.rt.(tc); ok {
		return p.TextureCreator()
	}
	return nil
}

// dirtyUnionToPixelRect converts a geometry.Rect (float32 logical pixels)
// to an image.Rectangle (integer physical pixels) for ggcanvas partial upload.
// Floor for min, ceil for max ensures the region fully covers the dirty area.
func dirtyUnionToPixelRect(r geometry.Rect) image.Rectangle {
	return image.Rect(
		int(math.Floor(float64(r.Min.X))),
		int(math.Floor(float64(r.Min.Y))),
		int(math.Ceil(float64(r.Max.X))),
		int(math.Ceil(float64(r.Max.Y))),
	)
}
