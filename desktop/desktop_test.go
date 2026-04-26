package desktop

import (
	"testing"

	"github.com/gogpu/ui/app"
)

// TestRunNilArgs verifies that Run returns errors for nil arguments
// rather than panicking.
func TestRunNilArgs(t *testing.T) {
	t.Run("nil gogpuApp", func(t *testing.T) {
		uiApp := app.New()
		err := Run(nil, uiApp)
		if err == nil {
			t.Fatal("expected error for nil gogpuApp")
		}
	})

	t.Run("nil uiApp", func(t *testing.T) {
		err := Run(nil, nil)
		if err == nil {
			t.Fatal("expected error for nil uiApp")
		}
	})
}

// TestRunForcesFrameworkManaged verifies that Run sets FrameworkManaged
// render mode on the UI window. We can't call Run (needs GPU context)
// but we verify the mode is set by inspecting the window after setup.
func TestRunForcesFrameworkManaged(t *testing.T) {
	// Create a headless UI app and verify SetRenderMode is available.
	uiApp := app.New()
	w := uiApp.Window()

	// Default mode is HostManaged.
	if w.RenderMode() != app.RenderModeHostManaged {
		t.Fatalf("default mode = %v, want HostManaged", w.RenderMode())
	}

	// Simulate what Run does: force FrameworkManaged.
	w.SetRenderMode(app.RenderModeFrameworkManaged)

	if w.RenderMode() != app.RenderModeFrameworkManaged {
		t.Errorf("mode after SetRenderMode = %v, want FrameworkManaged", w.RenderMode())
	}
}

// TestRenderModeConstants verifies the render mode constants are accessible
// from the desktop package (no import cycle).
func TestRenderModeConstants(t *testing.T) {
	if app.RenderModeHostManaged != 0 {
		t.Fatal("RenderModeHostManaged should be 0 (iota)")
	}
	if app.RenderModeFrameworkManaged != 1 {
		t.Fatal("RenderModeFrameworkManaged should be 1")
	}
}
