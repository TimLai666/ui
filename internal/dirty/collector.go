package dirty

import (
	"github.com/gogpu/ui/geometry"
	"github.com/gogpu/ui/widget"
)

// Collector walks the widget tree and collects dirty regions from widgets
// that have NeedsRedraw set. It populates a Tracker with the bounds of
// each dirty widget.
type Collector struct {
	tracker *Tracker
}

// NewCollector creates a new Collector that writes dirty regions to the
// given tracker.
func NewCollector(tracker *Tracker) *Collector {
	return &Collector{tracker: tracker}
}

// Collect walks the widget tree starting from root, adding the bounds of
// any widget with NeedsRedraw() == true to the tracker.
//
// Widgets that do not implement the NeedsRedraw interface (i.e., they do
// not embed [widget.WidgetBase]) are treated as always dirty for safety,
// matching the behavior of [widget.NeedsRedrawInTree].
//
// Invisible widgets and their subtrees are skipped.
func (c *Collector) Collect(root widget.Widget) {
	if root == nil {
		return
	}
	c.collect(root)
}

// collect is the recursive tree walker.
func (c *Collector) collect(w widget.Widget) {
	// Skip invisible widgets entirely.
	if vis, ok := w.(interface{ IsVisible() bool }); ok && !vis.IsVisible() {
		return
	}

	// Check if this widget needs redraw.
	dirty := c.isWidgetDirty(w)
	if dirty {
		c.markWidgetDirty(w)
	}

	// Recurse into children.
	for _, child := range w.Children() {
		c.collect(child)
	}
}

// isWidgetDirty returns true if the widget needs redrawing.
// Widgets without a NeedsRedraw method (no WidgetBase) are always considered dirty.
func (c *Collector) isWidgetDirty(w widget.Widget) bool {
	type needsRedrawer interface {
		NeedsRedraw() bool
	}
	nr, ok := w.(needsRedrawer)
	if !ok {
		return true // no WidgetBase — assume dirty
	}
	return nr.NeedsRedraw()
}

// markWidgetDirty adds the widget's screen-space bounds to the tracker.
// ScreenBounds (set during the previous Draw pass via StampScreenOrigin)
// gives the global position needed for canvas-level dirty clipping.
// This is safe because layout changes always set needsFullRepaint=true,
// which bypasses dirty-region clipping entirely — so ScreenBounds from
// the previous frame is always valid when this code path executes.
// Follows Qt QWidgetRepaintManager::markDirty pattern: translate
// widget-local rect to top-level window coordinates at collection time.
func (c *Collector) markWidgetDirty(w widget.Widget) {
	type screenBounder interface {
		ScreenBounds() geometry.Rect
	}
	if sb, ok := w.(screenBounder); ok {
		c.tracker.MarkDirty(sb.ScreenBounds())
		return
	}
	type bounder interface {
		Bounds() geometry.Rect
	}
	if b, ok := w.(bounder); ok {
		c.tracker.MarkDirty(b.Bounds())
	}
}
