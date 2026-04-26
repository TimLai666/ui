package listview

import (
	"github.com/gogpu/ui/cdk"
	"github.com/gogpu/ui/primitives"
	"github.com/gogpu/ui/widget"
)

// widgetCache caches the currently visible item widgets between frames.
//
// When the visible range has not changed and data has not been invalidated,
// the cache returns the same widgets without calling the builder again.
// This avoids unnecessary allocations during static frames (no scroll).
//
// Each item widget is automatically wrapped in a [primitives.RepaintBoundary]
// to enable per-item pixel caching. When the item subtree is clean (no dirty
// widgets), the cached pixels are blitted directly instead of re-rendering.
// Item background, selection, and dividers are painted OUTSIDE the boundary
// by the painter on the main canvas.
type widgetCache struct {
	startIndex int
	endIndex   int
	widgets    []widget.Widget
	boundaries []*primitives.RepaintBoundary
	valid      bool
}

// update ensures the cache contains widgets for the range [start, end).
// If the range matches and the cache is valid, this is a no-op.
// Otherwise, it calls the content's Render method for each index in the range
// and wraps each widget in a RepaintBoundary.
func (wc *widgetCache) update(start, end int, content cdk.Content[ItemContext], selectedIndex, hoveredIndex int) {
	count := end - start
	if count <= 0 {
		wc.clear()
		return
	}

	// Check if cache can be reused.
	if wc.valid && wc.startIndex == start && wc.endIndex == end {
		return
	}

	// Rebuild cache.
	if cap(wc.widgets) >= count {
		wc.widgets = wc.widgets[:count]
	} else {
		wc.widgets = make([]widget.Widget, count)
	}

	if cap(wc.boundaries) >= count {
		wc.boundaries = wc.boundaries[:count]
	} else {
		wc.boundaries = make([]*primitives.RepaintBoundary, count)
	}

	if content == nil {
		for i := range wc.widgets {
			wc.widgets[i] = nil
			wc.boundaries[i] = nil
		}
	} else {
		for i := range count {
			idx := start + i
			w := content.Render(ItemContext{
				Index:    idx,
				Selected: idx == selectedIndex,
				Focused:  idx == selectedIndex,
				Hovered:  idx == hoveredIndex,
			})
			wc.widgets[i] = w
			if w != nil {
				wc.boundaries[i] = primitives.NewRepaintBoundary(w)
			} else {
				wc.boundaries[i] = nil
			}
		}
	}

	wc.startIndex = start
	wc.endIndex = end
	wc.valid = true
}

// widgetAt returns the cached widget at the given offset from startIndex.
// This returns the raw (unwrapped) widget for compatibility with existing code
// that needs direct widget access (e.g., for Layout and bounds setting).
func (wc *widgetCache) widgetAt(offset int) widget.Widget {
	if offset < 0 || offset >= len(wc.widgets) {
		return nil
	}
	return wc.widgets[offset]
}

// boundaryAt returns the RepaintBoundary wrapper for the widget at the given
// offset from startIndex. Returns nil if the offset is out of range or the
// widget at that offset is nil.
func (wc *widgetCache) boundaryAt(offset int) *primitives.RepaintBoundary {
	if offset < 0 || offset >= len(wc.boundaries) {
		return nil
	}
	return wc.boundaries[offset]
}

// invalidate marks the cache as needing a rebuild.
func (wc *widgetCache) invalidate() {
	wc.valid = false
}

// clear resets the cache entirely and unmounts boundaries to free pixel caches.
func (wc *widgetCache) clear() {
	// Unmount boundaries to release pixel caches.
	for i := range wc.boundaries {
		if wc.boundaries[i] != nil {
			wc.boundaries[i].Unmount()
		}
		wc.boundaries[i] = nil
	}
	wc.boundaries = wc.boundaries[:0]

	// Clear widget references for GC.
	for i := range wc.widgets {
		wc.widgets[i] = nil
	}
	wc.widgets = wc.widgets[:0]
	wc.startIndex = 0
	wc.endIndex = 0
	wc.valid = false
}
