package offscreen_test

import (
	"image"
	"testing"

	"github.com/gogpu/ui/offscreen"
	"github.com/gogpu/ui/primitives"
	"github.com/gogpu/ui/theme/material3"
	"github.com/gogpu/ui/widget"
)

func TestNewRenderer(t *testing.T) {
	r := offscreen.NewRenderer(400, 120)
	if r == nil {
		t.Fatal("NewRenderer returned nil")
	}
	if r.Image() != nil {
		t.Error("Image() should be nil before Render")
	}
}

func TestNewRenderer_ClampsDimensions(t *testing.T) {
	r := offscreen.NewRenderer(0, -5)
	r.Render(primitives.Text("ok"))
	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil for clamped dimensions")
	}
	bounds := img.Bounds()
	if bounds.Dx() < 1 || bounds.Dy() < 1 {
		t.Errorf("clamped dimensions should be >= 1x1, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestRenderText(t *testing.T) {
	r := offscreen.NewRenderer(400, 120)
	r.Render(primitives.Text("Hello, World!").FontSize(24))

	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil after Render")
	}
	if img.Bounds() != image.Rect(0, 0, 400, 120) {
		t.Errorf("unexpected bounds: %v", img.Bounds())
	}
	if isBlank(img) {
		t.Error("rendered image is blank — text was not drawn")
	}
}

func TestRenderText_EmptyContent(t *testing.T) {
	r := offscreen.NewRenderer(100, 50)
	r.Render(primitives.Text(""))

	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil for empty text")
	}
}

func TestRenderBox(t *testing.T) {
	box := primitives.Box(
		primitives.Text("Inside box"),
	).Background(widget.ColorBlue).Padding(10)

	r := offscreen.NewRenderer(300, 100)
	r.Render(box)

	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil")
	}
	if isBlank(img) {
		t.Error("rendered box image is blank")
	}
}

func TestWithTheme(t *testing.T) {
	dark := material3.NewDark(widget.Hex(0x00FF00))
	r := offscreen.NewRenderer(200, 60, offscreen.WithTheme(dark))
	r.Render(primitives.Text("Dark theme").FontSize(16))

	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil")
	}
	if isBlank(img) {
		t.Error("rendered image is blank with custom theme")
	}
}

func TestWithScale(t *testing.T) {
	r := offscreen.NewRenderer(200, 100, offscreen.WithScale(2.0))
	r.Render(primitives.Text("HiDPI").FontSize(14))

	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil")
	}
	if isBlank(img) {
		t.Error("HiDPI rendered image is blank")
	}
}

func TestWithScale_IgnoresNonPositive(t *testing.T) {
	r := offscreen.NewRenderer(100, 50, offscreen.WithScale(0), offscreen.WithScale(-1))
	r.Render(primitives.Text("ok"))
	if r.Image() == nil {
		t.Fatal("Image() returned nil")
	}
}

func TestWithBackground(t *testing.T) {
	r := offscreen.NewRenderer(100, 50, offscreen.WithBackground(widget.ColorWhite))
	r.Render(primitives.Text(""))

	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil")
	}
	if isBlank(img) {
		t.Error("white background should produce non-blank image")
	}
	// Verify a corner pixel is white (opaque).
	rr, g, b, a := img.At(0, 0).RGBA()
	if a == 0 {
		t.Error("background pixel should be opaque")
	}
	if rr < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Errorf("expected near-white corner, got RGBA(%d,%d,%d,%d)", rr, g, b, a)
	}
}

func TestRenderReplacesPreviousImage(t *testing.T) {
	r := offscreen.NewRenderer(100, 50)

	r.Render(primitives.Text("First"))
	img1 := r.Image()

	r.Render(primitives.Text("Second"))
	img2 := r.Image()

	if img1 == img2 {
		t.Error("Render should replace the previous image")
	}
}

func TestRenderMultipleOptions(t *testing.T) {
	dark := material3.NewDark(widget.Hex(0xFF5722))
	r := offscreen.NewRenderer(400, 200,
		offscreen.WithTheme(dark),
		offscreen.WithScale(1.5),
		offscreen.WithBackground(widget.Hex(0x121212)),
	)
	r.Render(
		primitives.Box(
			primitives.Text("Title").FontSize(20).Bold(),
			primitives.Text("Subtitle").FontSize(14),
		).Padding(16),
	)

	img := r.Image()
	if img == nil {
		t.Fatal("Image() returned nil")
	}
	if isBlank(img) {
		t.Error("complex render with multiple options is blank")
	}
}

// isBlank reports whether every pixel in the image has zero alpha.
func isBlank(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 {
			return false
		}
	}
	return true
}
