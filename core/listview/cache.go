package listview

import (
	"github.com/gogpu/ui/cdk"
	"github.com/gogpu/ui/widget"
)

// widgetCache caches the currently visible item widgets between frames.
//
// When the visible range has not changed and data has not been invalidated,
// the cache returns the same widgets without calling the builder again.
// This avoids unnecessary allocations during static frames (no scroll).
type widgetCache struct {
	startIndex int
	endIndex   int
	widgets    []widget.Widget
	valid      bool
}

// update ensures the cache contains widgets for the range [start, end).
// If the range matches and the cache is valid, this is a no-op.
// Otherwise, it calls the content's Render method for each index in the range.
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

	if content == nil {
		for i := range wc.widgets {
			wc.widgets[i] = nil
		}
	} else {
		for i := range count {
			idx := start + i
			wc.widgets[i] = content.Render(ItemContext{
				Index:    idx,
				Selected: idx == selectedIndex,
				Focused:  idx == selectedIndex,
				Hovered:  idx == hoveredIndex,
			})
		}
	}

	wc.startIndex = start
	wc.endIndex = end
	wc.valid = true
}

// widgetAt returns the cached widget at the given offset from startIndex.
func (wc *widgetCache) widgetAt(offset int) widget.Widget {
	if offset < 0 || offset >= len(wc.widgets) {
		return nil
	}
	return wc.widgets[offset]
}

// invalidate marks the cache as needing a rebuild.
func (wc *widgetCache) invalidate() {
	wc.valid = false
}

// clear resets the cache entirely.
func (wc *widgetCache) clear() {
	// Clear references for GC.
	for i := range wc.widgets {
		wc.widgets[i] = nil
	}
	wc.widgets = wc.widgets[:0]
	wc.startIndex = 0
	wc.endIndex = 0
	wc.valid = false
}
