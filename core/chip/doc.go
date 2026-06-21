// Package chip provides a compact chip widget for tags, filters, and inline
// actions, following the Material 3 chip guidelines.
//
// A chip is a small, interactive, pill-shaped element. It can act as a simple
// action (assist/suggestion chip) or as a toggleable filter (filter chip).
//
// Construction uses functional options for immutable configuration, while
// fluent methods handle mutable styling:
//
//	c := chip.New(
//	    chip.Label("In stock"),
//	    chip.Selectable(true),
//	    chip.OnSelectedChanged(func(sel bool) { applyFilter(sel) }),
//	).Padding(2)
//
// # Modes
//
//   - Action chip (default): clicking invokes the [OnClick] handler. Use for
//     assist or suggestion chips.
//   - Filter chip ([Selectable] is true): clicking toggles the selected state
//     and invokes [OnSelectedChanged]. The chip renders a filled background
//     when selected and an outlined background when not.
//
// # Controlled vs uncontrolled selection
//
// When [Selected] is bound to a function or signal, the chip is "controlled":
// the owner is responsible for updating that source in response to
// [OnSelectedChanged]. With only a static initial value, the chip is
// "uncontrolled" and toggles its own internal state on activation.
//
// # Visual Style
//
// The visual rendering is provided by a [Painter] implementation. Each design
// system (Material 3, Fluent, Cupertino) may supply its own painter. If no
// painter is set, [DefaultPainter] is used.
//
// # Signal Binding
//
// Chip properties can be bound to reactive signals from the [state] package:
//
//   - [LabelSignal] / [LabelReadonlySignal] / [LabelFn] -- the label text
//   - [SelectedSignal] / [SelectedReadonlySignal] / [SelectedFn] -- selected state
//   - [DisabledSignal] / [DisabledReadonlySignal] / [DisabledFn] -- disabled state
//
// # Accessibility
//
// A chip is focusable when visible, enabled, and not disabled. It activates on
// the Enter and Space keys, matching the button widget.
package chip
