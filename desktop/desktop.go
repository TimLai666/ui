package desktop

import (
	"fmt"
	"log"

	"github.com/gogpu/gg"
	"github.com/gogpu/gg/integration/ggcanvas"
	"github.com/gogpu/gogpu"
	"github.com/gogpu/ui/app"
	"github.com/gogpu/ui/render"
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
		gg.CloseAccelerator()
		if rl.canvas != nil {
			_ = rl.canvas.Close()
		}
	})

	return gogpuApp.Run()
}

// renderLoop holds the state for the scene-composition render loop.
type renderLoop struct {
	gogpuApp *gogpu.App
	uiApp    *app.App
	canvas   *ggcanvas.Canvas
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
	}

	win := rl.uiApp.Window()
	cc := rl.canvas.Context()

	// Surface dimensions may differ from canvas by 1-2px (integer rounding).
	sw, sh := dc.SurfaceSize()

	gg.BeginAcceleratorFrame()
	cc.BeginGPUFrame()

	// Clear background covering the full surface area.
	// GPU render pass LoadOpClear uses transparent black — we must cover
	// every pixel with the theme background to avoid black edges.
	bg := win.ThemeBackground()
	cc.SetRGBA(float64(bg.R), float64(bg.G), float64(bg.B), float64(bg.A))
	cc.DrawRectangle(0, 0, float64(sw), float64(sh))
	_ = cc.Fill()

	// Full tree draw. RepaintBoundary cache hits replay cached scene.Scene
	// via render.Canvas.ReplayScene (Push/Translate/GPUSceneRenderer/Pop).
	// All GPU shapes (SDF, text, paths) are queued into gg.Context pipeline.
	widgetCanvas := render.NewCanvas(cc, cw, ch)
	win.DrawTo(widgetCanvas)
	win.PaintDirtyBoundaries()

	// Single render pass → surface.
	sv := dc.RenderTarget().SurfaceView()
	if sv.IsNil() {
		return
	}
	if err := cc.FlushGPUWithView(sv, sw, sh); err != nil {
		log.Printf("desktop: FlushGPUWithView: %v", err)
	}
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
