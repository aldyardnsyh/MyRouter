//go:build !windows

package tray

import "errors"

// ErrUnsupported is returned when the tray is requested on a non-Windows host.
var ErrUnsupported = errors.New("system tray is only supported on Windows")

// Run is a no-op on non-Windows platforms.
func Run(Options) error { return ErrUnsupported }

// Stop is a no-op on non-Windows platforms.
func Stop() {}

// InstallShortcut is unsupported on non-Windows platforms.
func InstallShortcut(string, bool) (string, error) { return "", ErrUnsupported }
