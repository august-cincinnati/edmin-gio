package main

import "gioui.org/io/key"

// shortcutMods reduces an event's modifiers to Ctrl, Shift, Alt and
// Command. Shortcuts use Ctrl on every platform, macOS included; Command is
// kept so that a Cmd combination never matches a Ctrl shortcut.
func shortcutMods(m key.Modifiers) key.Modifiers {
	return m & (key.ModCtrl | key.ModShift | key.ModAlt | key.ModCommand)
}

// panelDigit returns which of the panel shortcut digits 1-5 a key is, or 0.
// With Shift held most layouts report the shifted symbol rather than the
// digit, so the US symbols above 1-4 count too. 5 has no Shift form, so its
// symbol is left out.
func panelDigit(n key.Name) int {
	switch n {
	case "1", "!":
		return 1
	case "2", "@":
		return 2
	case "3", "#":
		return 3
	case "4", "$":
		return 4
	case "5":
		return 5
	}
	return 0
}
