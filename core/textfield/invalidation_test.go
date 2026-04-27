package textfield

import (
	"testing"

	"github.com/gogpu/ui/event"
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/widget"
)

// --- Granular Invalidation Tests (TASK-UI-INVAL-001k) ---
//
// These tests verify that hover enter/leave use granular invalidation
// (SetNeedsRedraw + InvalidateRect) instead of full-tree ctx.Invalidate().
// Text input (handleCharInput) and mouse press (focus request) KEEP full
// invalidation because they modify content or require layout changes.

func TestGranularInvalidation_HoverEnter_NoFullInvalidate(t *testing.T) {
	w := New()
	w.SetBounds(geometry.NewRect(0, 0, 300, 48))
	ctx := widget.NewContext()

	enter := event.NewMouseEvent(event.MouseEnter, event.ButtonNone, 0,
		geometry.Pt(150, 24), geometry.Pt(150, 24), event.ModNone)
	handleEvent(w, ctx, enter)

	if ctx.IsInvalidated() {
		t.Error("MouseEnter should NOT trigger full invalidation (ctx.Invalidate)")
	}
	if !w.NeedsRedraw() {
		t.Error("MouseEnter should set needsRedraw on widget")
	}
	if ctx.InvalidatedRect().IsEmpty() {
		t.Error("MouseEnter should trigger InvalidateRect with widget bounds")
	}
	if !w.hovered {
		t.Error("hovered should be true after MouseEnter")
	}
}

func TestGranularInvalidation_HoverLeave_NoFullInvalidate(t *testing.T) {
	w := New()
	w.SetBounds(geometry.NewRect(0, 0, 300, 48))

	// Enter first to set hovered state.
	ctx := widget.NewContext()
	enter := event.NewMouseEvent(event.MouseEnter, event.ButtonNone, 0,
		geometry.Pt(150, 24), geometry.Pt(150, 24), event.ModNone)
	handleEvent(w, ctx, enter)

	// Reset context and redraw flag.
	ctx = widget.NewContext()
	w.ClearRedraw()

	leave := event.NewMouseEvent(event.MouseLeave, event.ButtonNone, 0,
		geometry.Pt(400, 24), geometry.Pt(400, 24), event.ModNone)
	handleEvent(w, ctx, leave)

	if ctx.IsInvalidated() {
		t.Error("MouseLeave should NOT trigger full invalidation")
	}
	if !w.NeedsRedraw() {
		t.Error("MouseLeave should set needsRedraw on widget")
	}
	if ctx.InvalidatedRect().IsEmpty() {
		t.Error("MouseLeave should trigger InvalidateRect")
	}
	if w.hovered {
		t.Error("hovered should be false after MouseLeave")
	}
}

func TestGranularInvalidation_MousePress_KeepsFullInvalidation(t *testing.T) {
	w := New()
	w.SetBounds(geometry.NewRect(0, 0, 300, 48))
	ctx := widget.NewContext()

	// Mouse press places cursor and requests focus -- structural.
	press := event.NewMouseEvent(event.MousePress, event.ButtonLeft, event.ButtonStateLeft,
		geometry.Pt(150, 24), geometry.Pt(150, 24), event.ModNone)
	handleEvent(w, ctx, press)

	if !ctx.IsInvalidated() {
		t.Error("MousePress MUST trigger full invalidation (focus + cursor placement)")
	}
}

func TestGranularInvalidation_TextInput_KeepsFullInvalidation(t *testing.T) {
	w := New()
	w.SetBounds(geometry.NewRect(0, 0, 300, 48))
	w.SetFocused(true)
	ctx := widget.NewContext()

	// Type a character.
	keyEvt := event.NewKeyEvent(event.KeyPress, event.KeyA, 'a', event.ModNone)
	handleEvent(w, ctx, keyEvt)

	if !ctx.IsInvalidated() {
		t.Error("text input MUST trigger full invalidation (content change needs layout)")
	}
}

func TestGranularInvalidation_InvalidateRect_MatchesBounds(t *testing.T) {
	bounds := geometry.NewRect(10, 20, 300, 48)
	w := New()
	w.SetBounds(bounds)
	ctx := widget.NewContext()

	enter := event.NewMouseEvent(event.MouseEnter, event.ButtonNone, 0,
		geometry.Pt(150, 44), geometry.Pt(150, 44), event.ModNone)
	handleEvent(w, ctx, enter)

	got := ctx.InvalidatedRect()
	if got != bounds {
		t.Errorf("InvalidatedRect = %v, want %v (widget bounds)", got, bounds)
	}
}
