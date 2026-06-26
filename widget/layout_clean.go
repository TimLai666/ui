package widget

// MarkLayoutCleanRecursive walks the widget tree rooted at w and marks every
// WidgetBase-embedding widget as layout-clean (layoutCacheValid = true).
//
// This is called by the Window after a full-tree layout pass so that
// MarkNeedsLayout()'s idempotency guard works before LayoutChild activates
// per-widget caching. Without this shim, layoutCacheValid is never set to
// true (its zero value is false), so MarkNeedsLayout() is a permanent no-op.
//
// When LayoutChild is adopted (PR 2b), layoutCacheStore handles the cache
// lifecycle and this function becomes redundant (harmless but unnecessary).
func MarkLayoutCleanRecursive(w Widget) {
	if w == nil {
		return
	}
	if lc, ok := w.(interface{ markLayoutClean() }); ok {
		lc.markLayoutClean()
	}
	for _, child := range w.Children() {
		MarkLayoutCleanRecursive(child)
	}
}

func (w *WidgetBase) markLayoutClean() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.layoutCacheValid = true
}
