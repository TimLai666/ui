package desktop

import (
	"testing"

	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/widget"
)

// --- Compositor Clip Tests ---
//
// These tests verify that walkBoundaries and compositeTextures respect
// CompositorClip — skipping boundary textures outside the viewport.
// This implements ScrollView clipping at compositor level.

// TestCompositorClip_SkipsItemsOutsideClip verifies that walkBoundaries
// skips items whose screen rect doesn't intersect their CompositorClip.
func TestCompositorClip_SkipsItemsOutsideClip(t *testing.T) {
	// Phase 1: Verify test setup.
	viewportClip := geometry.NewRect(0, 200, 800, 300)
	t.Logf("viewport clip: %v (Min=%v Max=%v)", viewportClip, viewportClip.Min, viewportClip.Max)

	if viewportClip.Height() != 300 {
		t.Fatalf("viewport clip height = %v, want 300", viewportClip.Height())
	}
	if viewportClip.Max.Y != 500 {
		t.Fatalf("viewport clip Max.Y = %v, want 500", viewportClip.Max.Y)
	}

	root := &ccTestContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	rootKey := root.BoundaryCacheKey()

	// Create items at specific screen positions relative to viewport.
	type itemSpec struct {
		screenY float32
		wantVis bool // should be visited by walkBoundaries?
	}
	specs := []itemSpec{
		{screenY: 100, wantVis: false}, // item 0: fully above (y:100-140 vs clip y:200-500)
		{screenY: 190, wantVis: true},  // item 1: partially above (y:190-230 ∩ y:200-500)
		{screenY: 300, wantVis: true},  // item 2: fully inside (y:300-340 ∈ y:200-500)
		{screenY: 480, wantVis: true},  // item 3: partially below (y:480-520 ∩ y:200-500)
		{screenY: 510, wantVis: false}, // item 4: fully below (y:510-550 vs clip y:200-500)
	}

	items := make([]*ccTestItem, len(specs))
	for i, s := range specs {
		items[i] = &ccTestItem{index: i}
		items[i].SetVisible(true)
		items[i].SetRepaintBoundary(true)
		items[i].SetBounds(geometry.NewRect(0, 0, 200, 40))
		items[i].SetScreenOrigin(geometry.Pt(10, s.screenY))
		items[i].SetCompositorClip(viewportClip)
		items[i].SetParent(root)
	}

	// Phase 2: Verify each item's stored clip is correct.
	for i, item := range items {
		clip := item.CompositorClip()
		if clip.Max.Y != 500 {
			t.Errorf("item[%d] stored clip Max.Y = %v, want 500 (clip=%v)", i, clip.Max.Y, clip)
		}
	}

	// Phase 3: Verify intersection logic independently.
	for i, s := range specs {
		origin := geometry.Pt(10, s.screenY)
		screenRect := geometry.Rect{
			Min: origin,
			Max: geometry.Pt(origin.X+200, origin.Y+40),
		}
		intersects := screenRect.Intersects(viewportClip)
		if intersects != s.wantVis {
			t.Errorf("item[%d] intersection: got %v, want %v (screen=%v clip=%v)",
				i, intersects, s.wantVis, screenRect, viewportClip)
		}
	}

	// Phase 4: Wire up widget tree.
	children := make([]widget.Widget, len(items))
	for i, item := range items {
		children[i] = item
	}
	root.children = children

	// Phase 5: Walk boundaries and verify clip filtering.
	itemKeys := make(map[uint64]int)
	for i, item := range items {
		itemKeys[item.BoundaryCacheKey()] = i
	}

	rl := &renderLoop{}
	var visited []int
	rl.walkBoundaries(root, func(key uint64, _ geometry.Point, _, _ int) {
		if key == rootKey {
			return
		}
		if idx, ok := itemKeys[key]; ok {
			visited = append(visited, idx)
		}
	})

	// Expected: items 1, 2, 3 visible; items 0, 4 clipped away.
	want := []int{1, 2, 3}
	if len(visited) != len(want) {
		t.Fatalf("visited %v, want %v", visited, want)
	}
	for i, idx := range visited {
		if idx != want[i] {
			t.Errorf("visited[%d] = %d, want %d", i, idx, want[i])
		}
	}
}

// TestCompositorClip_NoClipShowsAll verifies backward compatibility:
// boundaries without CompositorClip are always composited.
func TestCompositorClip_NoClipShowsAll(t *testing.T) {
	root := &ccTestContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	rootKey := root.BoundaryCacheKey()

	item0 := &ccTestItem{index: 0}
	item0.SetVisible(true)
	item0.SetRepaintBoundary(true)
	item0.SetBounds(geometry.NewRect(0, 0, 200, 40))
	item0.SetScreenOrigin(geometry.Pt(10, 100))
	// No SetCompositorClip — should always be visible.
	item0.SetParent(root)

	item1 := &ccTestItem{index: 1}
	item1.SetVisible(true)
	item1.SetRepaintBoundary(true)
	item1.SetBounds(geometry.NewRect(0, 0, 200, 40))
	item1.SetScreenOrigin(geometry.Pt(10, 700))
	// No SetCompositorClip — should always be visible.
	item1.SetParent(root)

	root.children = []widget.Widget{item0, item1}

	rl := &renderLoop{}
	var count int
	rl.walkBoundaries(root, func(key uint64, _ geometry.Point, _, _ int) {
		if key != rootKey {
			count++
		}
	})

	if count != 2 {
		t.Errorf("without CompositorClip, all items should be visible: got %d, want 2", count)
	}
}

// TestCompositorClip_RootNeverClipped verifies that the root boundary
// (depth=0) is never affected by compositor clip.
func TestCompositorClip_RootNeverClipped(t *testing.T) {
	root := &ccTestContainer{}
	root.SetVisible(true)
	root.SetRepaintBoundary(true)
	root.SetBounds(geometry.NewRect(0, 0, 800, 600))
	root.SetCompositorClip(geometry.NewRect(0, 0, 1, 1)) // tiny clip

	rl := &renderLoop{}
	var rootVisited bool
	rl.walkBoundaries(root, func(key uint64, _ geometry.Point, _, _ int) {
		if key == root.BoundaryCacheKey() {
			rootVisited = true
		}
	})

	if !rootVisited {
		t.Error("root boundary should never be clipped (depth=0)")
	}
}

// --- test helpers ---

type ccTestItem struct {
	widget.WidgetBase
	index int
}

func (w *ccTestItem) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(200, 40))
}

func (w *ccTestItem) Draw(_ widget.Context, _ widget.Canvas) {}

func (w *ccTestItem) Event(_ widget.Context, _ event.Event) bool { return false }

func (w *ccTestItem) Children() []widget.Widget { return nil }

type ccTestContainer struct {
	widget.WidgetBase
	children []widget.Widget
}

func (w *ccTestContainer) Layout(_ widget.Context, c geometry.Constraints) geometry.Size {
	return c.Constrain(geometry.Sz(800, 600))
}

func (w *ccTestContainer) Draw(_ widget.Context, _ widget.Canvas) {}

func (w *ccTestContainer) Event(_ widget.Context, _ event.Event) bool { return false }

func (w *ccTestContainer) Children() []widget.Widget { return w.children }
