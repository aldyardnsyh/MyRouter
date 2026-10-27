//go:build !windows

package tray

import (
	"fmt"
	"os"
)

// Run is a no-op on non-Windows platforms.
func Run(Options) error { return ErrUnsupported }

// Stop is a no-op on non-Windows platforms.
func Stop() {}

// HasConsole reports whether the process has an attached console.
func HasConsole() bool { return false }

// Interactive reports whether a desktop tray can exist on this platform.
func Interactive() bool { return false }

// HideOwnConsole is a no-op on non-Windows platforms.
func HideOwnConsole() bool { return false }

// ShowError writes the message to stderr on non-Windows platforms.
func ShowError(title, text string) { _, _ = fmt.Fprintf(os.Stderr, "%s: %s\n", title, text) }

// InstallShortcut is unsupported on non-Windows platforms.
func InstallShortcut(string, bool) (string, error) { return "", ErrUnsupported }
