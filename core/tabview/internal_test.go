package tabview

import (
	"testing"

	"github.com/gogpu/ui/geometry"
)

// --- Config Tests ---

func TestConfig_ResolvedSelected_Static(t *testing.T) {
	c := config{selected: 2}
	if got := c.ResolvedSelected(); got != 2 {
		t.Errorf("ResolvedSelected() = %d, want 2", got)
	}
}

func TestConfig_SetSelected(t *testing.T) {
	c := config{selected: 0}
	c.setSelected(3)
	if c.selected != 3 {
		t.Errorf("selected = %d, want 3", c.selected)
	}
}

// --- Tab Position Constants ---

func TestTabPosition_Constants(t *testing.T) {
	if Top != 0 {
		t.Errorf("Top = %d, want 0", Top)
	}
	if Bottom != 1 {
		t.Errorf("Bottom = %d, want 1", Bottom)
	}
}

// --- computeTabLayout ---

func TestComputeTabLayout_TopPosition(t *testing.T) {
	tabs := []Tab{
		{Label: "Tab1"},
		{Label: "Tab2"},
	}
	w := New(tabs, PositionOpt(Top))
	w.SetBounds(geometry.NewRect(0, 0, 200, 300))
	w.computeTabLayout(geometry.Sz(200, 300))

	if w.tabBarBounds.Min.Y != 0 {
		t.Errorf("tab bar Y = %v, want 0 (top)", w.tabBarBounds.Min.Y)
	}
	if w.tabBarBounds.Height() != tabBarHeight {
		t.Errorf("tab bar height = %v, want %v", w.tabBarBounds.Height(), tabBarHeight)
	}
}

func TestComputeTabLayout_BottomPosition(t *testing.T) {
	tabs := []Tab{
		{Label: "Tab1"},
		{Label: "Tab2"},
	}
	w := New(tabs, PositionOpt(Bottom))
	w.SetBounds(geometry.NewRect(0, 0, 200, 300))
	w.computeTabLayout(geometry.Sz(200, 300))

	expectedY := float32(300) - tabBarHeight
	if w.tabBarBounds.Min.Y != expectedY {
		t.Errorf("tab bar Y = %v, want %v (bottom)", w.tabBarBounds.Min.Y, expectedY)
	}
}

func TestComputeTabLayout_EqualWidthTabs(t *testing.T) {
	tabs := []Tab{
		{Label: "Tab1"},
		{Label: "Tab2"},
		{Label: "Tab3"},
	}
	w := New(tabs)
	w.SetBounds(geometry.NewRect(0, 0, 300, 300))
	w.computeTabLayout(geometry.Sz(300, 300))

	expectedWidth := float32(100) // 300 / 3
	for i, ts := range w.tabStates {
		gotWidth := ts.Bounds.Width()
		if gotWidth != expectedWidth {
			t.Errorf("tab[%d] width = %v, want %v", i, gotWidth, expectedWidth)
		}
	}
}

func TestComputeTabLayout_EmptyTabs(t *testing.T) {
	w := New(nil)
	w.SetBounds(geometry.NewRect(0, 0, 200, 300))
	w.computeTabLayout(geometry.Sz(200, 300))

	if len(w.tabStates) != 0 {
		t.Errorf("tabStates len = %d, want 0", len(w.tabStates))
	}
}

func TestComputeTabLayout_CloseButtonBounds(t *testing.T) {
	tabs := []Tab{
		{Label: "Tab1"},
		{Label: "Tab2"},
	}
	w := New(tabs, Closeable(true))
	w.SetBounds(geometry.NewRect(0, 0, 200, 300))
	w.computeTabLayout(geometry.Sz(200, 300))

	for i, ts := range w.tabStates {
		if ts.CloseButtonBounds.IsEmpty() {
			t.Errorf("tab[%d] close button bounds should not be empty when closeable", i)
		}
	}
}

func TestComputeTabLayout_DisabledNoCloseButton(t *testing.T) {
	tabs := []Tab{
		{Label: "Tab1", Disabled: true},
	}
	w := New(tabs, Closeable(true))
	w.SetBounds(geometry.NewRect(0, 0, 200, 300))
	w.computeTabLayout(geometry.Sz(200, 300))

	if !w.tabStates[0].CloseButtonBounds.IsEmpty() {
		t.Error("disabled tab should not have close button bounds")
	}
}

// --- contentBounds ---

func TestContentBounds_TopPosition(t *testing.T) {
	tabs := []Tab{{Label: "Tab1"}}
	w := New(tabs, PositionOpt(Top))
	w.SetBounds(geometry.NewRect(0, 0, 200, 300))

	cb := w.contentBounds(geometry.Sz(200, 300))

	if cb.Min.Y != tabBarHeight {
		t.Errorf("content top = %v, want %v", cb.Min.Y, tabBarHeight)
	}
	contentHeight := float32(300) - tabBarHeight
	if cb.Height() != contentHeight {
		t.Errorf("content height = %v, want %v", cb.Height(), contentHeight)
	}
}

func TestContentBounds_BottomPosition(t *testing.T) {
	tabs := []Tab{{Label: "Tab1"}}
	w := New(tabs, PositionOpt(Bottom))
	w.SetBounds(geometry.NewRect(0, 0, 200, 300))

	cb := w.contentBounds(geometry.Sz(200, 300))

	if cb.Min.Y != 0 {
		t.Errorf("content top = %v, want 0", cb.Min.Y)
	}
	contentHeight := float32(300) - tabBarHeight
	if cb.Height() != contentHeight {
		t.Errorf("content height = %v, want %v", cb.Height(), contentHeight)
	}
}

// --- updateTabStates ---

func TestUpdateTabStates(t *testing.T) {
	tabs := []Tab{
		{Label: "Tab1"},
		{Label: "Tab2", Disabled: true, Closeable: true},
	}
	w := New(tabs)
	w.tabStates = make([]TabState, len(tabs))
	w.updateTabStates(0)

	if w.tabStates[0].Label != "Tab1" {
		t.Errorf("tab[0] label = %q, want %q", w.tabStates[0].Label, "Tab1")
	}
	if !w.tabStates[0].Selected {
		t.Error("tab[0] should be selected")
	}
	if w.tabStates[1].Selected {
		t.Error("tab[1] should not be selected")
	}
	if !w.tabStates[1].Disabled {
		t.Error("tab[1] should be disabled")
	}
	if !w.tabStates[1].Closeable {
		t.Error("tab[1] should be closeable (per-tab override)")
	}
}

// --- DefaultPainter ---

func TestDefaultPainter_EmptyBounds(t *testing.T) {
	p := DefaultPainter{}
	// Should not panic with empty bounds.
	p.PaintTabBar(nil, PaintState{})
}
