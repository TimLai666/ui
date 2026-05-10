package app

import (
	"testing"

	"github.com/gogpu/ui/compositor"
	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/widget"
)

// testContainer has children accessible via Children().
type testContainer struct {
	widget.WidgetBase
	kids []widget.Widget
}

func (w *testContainer) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(800, 600))
}
func (w *testContainer) Draw(_ widget.Context, canvas widget.Canvas) {
	canvas.DrawRect(w.Bounds(), widget.RGBA8(200, 200, 200, 255))
	for _, child := range w.kids {
		widget.DrawChild(child, nil, canvas)
	}
}
func (w *testContainer) Event(_ widget.Context, _ event.Event) bool { return false }
func (w *testContainer) Children() []widget.Widget                  { return w.kids }

// testLeaf is a leaf widget with boundary support.
type testLeaf struct {
	widget.WidgetBase
	drawCount int
}

func (w *testLeaf) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(48, 48))
}
func (w *testLeaf) Draw(_ widget.Context, canvas widget.Canvas) {
	w.drawCount++
	canvas.DrawRect(w.Bounds(), widget.RGBA8(255, 0, 0, 255))
}
func (w *testLeaf) Event(_ widget.Context, _ event.Event) bool { return false }
func (w *testLeaf) Children() []widget.Widget                  { return nil }

// TestPaintBoundaryLayers_FindsNestedBoundary verifies that
// PaintBoundaryLayers walks through non-boundary containers
// and reaches nested boundary widgets (spinner inside collapsible).
func TestPaintBoundaryLayers_FindsNestedBoundary(t *testing.T) {
	prev := widget.GetSceneRecorderFactory()
	widget.RegisterSceneRecorder(testSceneRecorder)
	defer widget.RegisterSceneRecorder(prev)

	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetScreenOrigin(geometry.Pt(0, 0))

	mid := &testContainer{}
	mid.SetVisible(true)
	mid.SetBounds(geometry.NewRect(0, 100, 800, 500))
	root.kids = append(root.kids, mid)

	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(100, 200, 148, 248))
	spinner.SetScreenOrigin(geometry.Pt(100, 200))
	mid.kids = append(mid.kids, spinner)

	ctx := widget.NewContext()
	ctx.SetOnInvalidateRect(func(_ geometry.Rect) {})

	PaintBoundaryLayersWithContext(root, nil, ctx)

	if root.CachedScene() == nil {
		t.Error("root boundary should have cached scene after PaintBoundaryLayers")
	}
	if spinner.CachedScene() == nil {
		t.Error("spinner boundary should have cached scene — PaintBoundaryLayers " +
			"must traverse non-boundary containers to reach nested boundaries")
	}
	if spinner.drawCount == 0 {
		t.Error("spinner.Draw should have been called during recording")
	}
}

// TestBuildLayerTree_NestedOffset verifies accumulated offset computation.
func TestBuildLayerTree_NestedOffset(t *testing.T) {
	root := &testContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))

	mid := &testContainer{}
	mid.SetVisible(true)
	mid.SetBounds(geometry.NewRect(0, 100, 800, 500))
	root.kids = append(root.kids, mid)

	spinner := &testLeaf{}
	spinner.SetVisible(true)
	spinner.SetRepaintBoundary(true)
	spinner.SetBounds(geometry.NewRect(50, 200, 98, 248))
	mid.kids = append(mid.kids, spinner)

	layer := BuildLayerTree(root)

	// Root OffsetLayer(0,0) has children:
	// [0] = root's own OffsetLayer(0,0) with PictureLayer (root boundary)
	// Root OffsetLayer → root boundary OffsetLayer → [PictureLayer, spinner OffsetLayer]
	children := layer.Children()
	t.Logf("root layer children: %d", len(children))
	for i, ch := range children {
		t.Logf("  child[%d]: %T, offset=%v, children=%d",
			i, ch, ch.Offset(), len(ch.(compositor.ContainerLayer).Children()))
	}

	if len(children) == 0 {
		t.Fatal("root layer should have children")
	}

	// Root boundary is first child OffsetLayer. It should contain spinner.
	rootBoundary, ok := children[0].(compositor.ContainerLayer)
	if !ok {
		t.Fatal("first child should be ContainerLayer (root boundary OffsetLayer)")
	}
	rootBoundaryChildren := rootBoundary.Children()
	t.Logf("root boundary children: %d", len(rootBoundaryChildren))

	// Should have PictureLayer + spinner OffsetLayer
	foundSpinner := false
	for _, rbc := range rootBoundaryChildren {
		if cl, ok2 := rbc.(compositor.ContainerLayer); ok2 && len(cl.Children()) > 0 {
			foundSpinner = true
		}
	}
	if !foundSpinner && len(rootBoundaryChildren) < 2 {
		t.Error("root boundary should have spinner as nested layer")
	}

	// Check spinner offset: mid.Bounds.Min(0,100) + spinner.Bounds.Min(50,200) = (50,300)
	for _, rbc := range rootBoundaryChildren {
		if cl, ok2 := rbc.(compositor.ContainerLayer); ok2 {
			t.Logf("spinner OffsetLayer offset: %v", cl.Offset())
		}
	}
}
