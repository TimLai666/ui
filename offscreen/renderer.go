package offscreen

import (
	"image"

	"github.com/gogpu/gg"
	"github.com/gogpu/ui/geometry"
	internalRender "github.com/gogpu/ui/internal/render"
	"github.com/gogpu/ui/render"
	"github.com/gogpu/ui/theme/material3"
	"github.com/gogpu/ui/widget"
)

// m3DefaultSeed is the Material 3 baseline purple used when no theme is provided.
var m3DefaultSeed = widget.Hex(0x6750A4)

// Renderer renders a widget tree into an offscreen image.
//
// Create with [NewRenderer]. Configure with [Option] functions.
// Call [Renderer.Render] to draw a widget, then [Renderer.Image] to
// retrieve the result.
type Renderer struct {
	width      int
	height     int
	scale      float32
	theme      widget.ThemeProvider
	background widget.Color
	img        *image.RGBA
}

// Option configures a [Renderer].
type Option func(*Renderer)

// WithTheme overrides the default Material 3 light theme.
func WithTheme(tp widget.ThemeProvider) Option {
	return func(r *Renderer) {
		r.theme = tp
	}
}

// WithScale sets the display scale factor for HiDPI rendering.
// The default is 1.0. A value of 2.0 renders at Retina density.
func WithScale(s float32) Option {
	return func(r *Renderer) {
		if s > 0 {
			r.scale = s
		}
	}
}

// WithBackground sets the canvas clear color before drawing.
// The default is transparent (zero alpha).
func WithBackground(c widget.Color) Option {
	return func(r *Renderer) {
		r.background = c
	}
}

// NewRenderer creates an offscreen renderer with the given pixel dimensions.
//
// Width and height must be positive; they are clamped to a minimum of 1.
// By default, a Material 3 light theme is applied and the background is
// transparent. Override with [WithTheme], [WithBackground], and [WithScale].
func NewRenderer(width, height int, opts ...Option) *Renderer {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	r := &Renderer{
		width:      width,
		height:     height,
		scale:      1.0,
		theme:      material3.New(m3DefaultSeed),
		background: widget.ColorTransparent,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Render lays out and draws the widget into the offscreen buffer.
//
// The widget receives the full renderer dimensions as loose layout
// constraints. Calling Render again replaces the previously rendered image.
func (r *Renderer) Render(w widget.Widget) {
	ctx := widget.NewContext()
	ctx.SetThemeProvider(r.theme)
	ctx.SetScale(r.scale)
	ctx.SetWindowSize(geometry.Sz(float32(r.width), float32(r.height)))

	constraints := geometry.Loose(geometry.Sz(float32(r.width), float32(r.height)))
	w.Layout(ctx, constraints)

	dc := gg.NewContext(r.width, r.height)
	canvas := render.NewCanvas(dc, r.width, r.height)
	canvas.Clear(r.background)

	widget.DrawTree(w, ctx, canvas)

	r.img = internalRender.ToRGBA(dc.Image())
	_ = dc.Close()
}

// Image returns the rendered image, or nil if [Render] has not been called.
func (r *Renderer) Image() *image.RGBA {
	return r.img
}
