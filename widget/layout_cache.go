package widget

import "github.com/gogpu/ui/geometry"

// Layout caching (ADR-032 Phase 1).
//
// These methods implement the layout-side analog of the RepaintBoundary
// scene cache (see boundary.go). A widget's measured size is cached keyed by
// the input constraints; [LayoutChild] reuses it when a parent re-measures a
// child with the same constraints, and [WidgetBase.MarkNeedsLayout] invalidates
// it when a layout-affecting property changes.
//
// Nothing in this package calls [LayoutChild] or [MarkNeedsLayout] yet — the
// mechanism is wired into containers and widgets in a follow-up. On its own it
// is inert and cannot change behavior.

// MarkNeedsLayout marks this widget's cached layout result as stale and
// propagates the invalidation up the parent chain, because an ancestor's size
// generally depends on this subtree's size. Propagation currently reaches the
// root (a RelayoutBoundary will bound it in Phase 2); the root notifies the
// Window via the callback registered with [WidgetBase.SetOnLayoutDirty].
//
// Per ADR-032 GAP-1, this marks paint-dirty on THIS widget only and does NOT
// propagate paint dirtiness upward. Paint propagation to the nearest
// RepaintBoundary happens in [LayoutChild] on the cache-miss path, after the
// widget's Layout has actually run — mirroring Flutter, where markNeedsLayout
// does not call markNeedsPaint.
//
// Widgets call this when a layout-affecting property changes (text/content,
// font size, padding, child set, explicit size), as distinct from
// [WidgetBase.SetNeedsRedraw] which is paint-only.
func (w *WidgetBase) MarkNeedsLayout() {
	w.mu.Lock()
	w.needsRedraw = true // self-only paint dirty (GAP-1)
	parent := w.parent
	w.mu.Unlock()

	w.InvalidateLayoutCache()
	invalidateLayoutCacheUpward(parent)
}

// InvalidateLayoutCache clears this node's cached layout result and, if this
// node is the root (has an onLayoutDirty callback), notifies the Window that a
// layout pass is needed. It does NOT propagate to ancestors — use
// [WidgetBase.MarkNeedsLayout] for that.
//
// The whole cache is zeroed, not just the validity flag (Android
// requestLayout/mMeasureCache.clear pattern), so a missed constraint
// comparison can never produce a stale hit.
func (w *WidgetBase) InvalidateLayoutCache() {
	w.mu.Lock()
	w.layoutCacheValid = false
	w.lastConstraints = geometry.Constraints{}
	w.lastSize = geometry.Size{}
	cb := w.onLayoutDirty
	w.mu.Unlock()

	if cb != nil {
		cb()
	}
}

// invalidateLayoutCacheUpward walks the parent chain from the given widget,
// clearing each ancestor's layout cache. It stops when the chain ends (root).
// Phase 2 will stop earlier at the nearest RelayoutBoundary.
func invalidateLayoutCacheUpward(w Widget) {
	type layoutCacheInvalidator interface{ InvalidateLayoutCache() }
	for w != nil {
		if inv, ok := w.(layoutCacheInvalidator); ok {
			inv.InvalidateLayoutCache()
		}
		pg, ok := w.(interface{ Parent() Widget })
		if !ok {
			return
		}
		w = pg.Parent()
	}
}

// SetOnLayoutDirty sets the callback invoked when layout invalidation reaches
// this node. The Window wires this on the root widget in SetRoot so that a
// descendant's MarkNeedsLayout schedules a layout pass — the layout-side
// analog of [WidgetBase.SetOnBoundaryDirty].
func (w *WidgetBase) SetOnLayoutDirty(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onLayoutDirty = fn
}

// IsLayoutCacheValid reports whether this widget currently has a valid cached
// layout result. Primarily for tests and the debug verifier.
func (w *WidgetBase) IsLayoutCacheValid() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.layoutCacheValid
}

// layoutCacheLookup returns the cached size if the cache is valid and was
// computed for exactly these constraints. Constraints is a value of four
// float32 fields, so equality is a direct, allocation-free comparison.
func (w *WidgetBase) layoutCacheLookup(c geometry.Constraints) (geometry.Size, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.layoutCacheValid && w.lastConstraints == c {
		return w.lastSize, true
	}
	return geometry.Size{}, false
}

// layoutCacheStore records the measured size for the given constraints.
func (w *WidgetBase) layoutCacheStore(c geometry.Constraints, size geometry.Size) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastConstraints = c
	w.lastSize = size
	w.layoutCacheValid = true
}
