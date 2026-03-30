// Example: gogpu/ui — Widget Demo
//
// Demonstrates the gogpu/ui widget toolkit rendering into a gogpu window
// using ggcanvas for GPU-accelerated 2D graphics.
//
// Architecture:
//
//	ui widgets → render.Canvas (gg) → ggcanvas → GPU surface (zero-copy)
//
// Rendering: event-driven (ContinuousRender=false).
// 0% CPU when idle. Redraws only on user interaction (click, key, resize).
//
// Requirements:
//   - gogpu v0.18.1+
//   - gg v0.28.1+
package main

import (
	"fmt"
	"log"

	"github.com/gogpu/gg"
	_ "github.com/gogpu/gg/gpu" // enable GPU SDF acceleration
	"github.com/gogpu/gg/integration/ggcanvas"
	"github.com/gogpu/gogpu"
	"github.com/gogpu/ui/app"
	"github.com/gogpu/ui/core/checkbox"
	"github.com/gogpu/ui/core/listview"
	"github.com/gogpu/ui/core/radio"
	"github.com/gogpu/ui/primitives"
	"github.com/gogpu/ui/render"
	"github.com/gogpu/ui/theme/material3"
	"github.com/gogpu/ui/widget"
)

func main() {
	// Create gogpu application with builder pattern.
	gogpuApp := gogpu.NewApp(gogpu.DefaultConfig().
		WithTitle("gogpu/ui — Widget Demo").
		WithSize(800, 900).
		WithContinuousRender(false)) // Event-driven: 0% CPU when idle

	// Create UI application wired to gogpu providers.
	uiApp := app.New(
		app.WithWindowProvider(gogpuApp),
		app.WithPlatformProvider(gogpuApp),
		app.WithEventSource(gogpuApp.EventSource()),
	)
	uiApp.SetRoot(buildUI())

	// Canvas for 2D rendering (created lazily).
	var canvas *ggcanvas.Canvas

	gogpuApp.OnDraw(func(dc *gogpu.Context) {
		w, h := dc.Width(), dc.Height()
		if w <= 0 || h <= 0 {
			return
		}

		// Lazy canvas initialization.
		if canvas == nil {
			provider := gogpuApp.GPUContextProvider()
			if provider == nil {
				return
			}
			var err error
			canvas, err = ggcanvas.New(provider, w, h)
			if err != nil {
				log.Printf("ggcanvas: %v", err)
				return
			}
		}

		uiApp.Frame()

		// Resize canvas to match window (handles resize inside draw frame).
		cw, ch := canvas.Size()
		if cw != w || ch != h {
			if err := canvas.Resize(w, h); err != nil {
				log.Printf("resize: %v", err)
			}
			cw, ch = w, h
		}

		// Surface is transient — draw every frame.
		sv := dc.SurfaceView()
		sw, sh := dc.SurfaceSize()

		// Set surface target BEFORE drawing so all GPU operations
		// (including mid-draw flushes) render to the surface.
		gg.SetAcceleratorSurfaceTarget(sv, sw, sh)

		canvas.Draw(func(cc *gg.Context) {
			// Background via GPU-accelerated filled rect.
			cc.SetRGBA(0.94, 0.94, 0.94, 1) // #F0F0F0
			cc.DrawRectangle(0, 0, float64(cw), float64(ch))
			cc.Fill()

			widgetCanvas := render.NewCanvas(cc, cw, ch)
			uiApp.Window().DrawTo(widgetCanvas)
		})

		// Render: GPU-direct on real GPUs, universal path on llvmpipe/software.
		if gg.AcceleratorCanRenderDirect() {
			if err := canvas.RenderDirect(sv, sw, sh); err != nil {
				log.Printf("render: %v", err)
			}
		} else {
			if err := canvas.Render(dc.RenderTarget()); err != nil {
				log.Printf("render: %v", err)
			}
		}
	})

	// GPU resources are automatically cleaned up on shutdown:
	// - ggcanvas.Canvas auto-registers with App's ResourceTracker
	// - App.Run() calls tracker.CloseAll() before Renderer.Destroy()
	// - OnClose is still available for additional cleanup if needed
	gogpuApp.OnClose(func() {
		gg.CloseAccelerator()
	})

	// Run application.
	if err := gogpuApp.Run(); err != nil {
		log.Fatal(err)
	}
}

func buildUI() *primitives.BoxWidget {
	// Card with content.
	card := primitives.Box(
		// Title.
		primitives.Text("gogpu/ui — Widget Demo").
			FontSize(28).
			Bold().
			Color(widget.RGBA8(33, 33, 33, 255)),

		// Checkbox section.
		primitives.Text("Checkboxes").
			FontSize(18).
			Bold().
			Color(widget.RGBA8(66, 66, 66, 255)),

		checkbox.New(
			checkbox.LabelOpt("Enable notifications"),
			checkbox.Checked(true),
			checkbox.OnToggle(func(checked bool) {
				fmt.Println("notifications:", checked)
			}),
		),

		checkbox.New(
			checkbox.LabelOpt("Dark mode"),
			checkbox.OnToggle(func(checked bool) {
				fmt.Println("dark mode:", checked)
			}),
		),

		checkbox.New(
			checkbox.LabelOpt("Disabled checkbox"),
			checkbox.Checked(true),
			checkbox.Disabled(true),
		),

		// Radio section.
		primitives.Text("Radio Buttons").
			FontSize(18).
			Bold().
			Color(widget.RGBA8(66, 66, 66, 255)),

		radio.NewGroup(
			radio.Items(
				radio.ItemDef{Value: "small", Label: "Small"},
				radio.ItemDef{Value: "medium", Label: "Medium"},
				radio.ItemDef{Value: "large", Label: "Large"},
			),
			radio.Selected("medium"),
			radio.OnChange(func(v string) {
				fmt.Println("size:", v)
			}),
		),

		// Horizontal radio.
		primitives.Text("Horizontal Radio").
			FontSize(14).
			Color(widget.RGBA8(100, 100, 100, 255)),

		radio.NewGroup(
			radio.Items(
				radio.ItemDef{Value: "light", Label: "Light"},
				radio.ItemDef{Value: "dark", Label: "Dark"},
				radio.ItemDef{Value: "system", Label: "System"},
			),
			radio.Selected("system"),
			radio.DirectionOpt(radio.Horizontal),
			radio.OnChange(func(v string) {
				fmt.Println("theme:", v)
			}),
		),

		// ListView section.
		primitives.Text("ListView (1000 items)").
			FontSize(18).
			Bold().
			Color(widget.RGBA8(66, 66, 66, 255)),

		// Wrap ListView in a Box with explicit height to ensure
		// it gets enough space in the vertical stack layout.
		// Box.Draw uses PushClipRoundRect (GPU SDF clip) to clip children
		// to rounded corners — no padding workaround needed.
		primitives.Box(buildListView()).
			Height(300).
			Rounded(8).
			Background(widget.RGBA8(250, 250, 250, 255)).
			BorderStyle(1, widget.RGBA8(218, 218, 218, 255)),
	).
		Padding(32).
		Gap(12).
		Background(widget.RGBA8(255, 255, 255, 255)).
		Rounded(12).
		ShadowLevel(2)

	// Outer container provides margin around the card.
	return primitives.Box(card).Padding(24)
}

// buildListView creates a ListView with 1000 items to demonstrate virtualization.
func buildListView() *listview.Widget {
	items := make([]string, 1000)
	for i := range items {
		items[i] = fmt.Sprintf("Item %d — Lorem ipsum dolor sit amet", i+1)
	}

	m3 := material3.New(widget.Hex(0x6750A4))
	return listview.New(
		listview.ItemCount(len(items)),
		listview.FixedItemHeight(36),
		listview.SelectionModeOpt(listview.SelectionSingle),
		listview.PainterOpt(material3.ListViewPainter{Theme: m3}),
		listview.BuildItem(func(ctx listview.ItemContext) widget.Widget {
			color := widget.RGBA8(33, 33, 33, 255)
			if ctx.Selected {
				color = widget.RGBA8(103, 80, 164, 255) // M3 primary
			}
			t := primitives.Text(items[ctx.Index]).
				FontSize(14).
				Color(color)
			if ctx.Selected {
				t = t.Bold()
			}
			return primitives.Box(t).PaddingXY(12, 8)
		}),
		listview.Divider(true),
		listview.OnItemClick(func(index int) {
			fmt.Printf("clicked: %s\n", items[index])
		}),
	)
}
