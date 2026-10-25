// Package tray provides a Windows system-tray icon plus desktop-shortcut
// helpers. The implementation is pure Go (no CGO) so the same binary can be
// cross-compiled for a VPS without a C toolchain.
package tray

import (
	"os/exec"
	"runtime"
)

// Options configures the tray icon.
type Options struct {
	// Tooltip is shown when hovering the icon.
	Tooltip string
	// DashboardURL opens the admin SPA.
	DashboardURL string
	// GatewayURL is copied by "Copy gateway URL".
	GatewayURL string
	// OnQuit runs before the icon is removed (used for graceful shutdown).
	OnQuit func()
	// OnReady is called once the icon is visible in the tray.
	OnReady func()
}

// OpenBrowser opens url in the user's default browser.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
