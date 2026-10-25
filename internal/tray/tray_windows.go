//go:build windows

package tray

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

//go:embed icon.ico
var iconBytes []byte

const (
	trayMsg = 0x8000 + 1 // WM_APP + 1

	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmNull          = 0x0000
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B

	nimAdd        = 0x00000000
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004
	nimVersion4   = 4
	ninSelect     = 0x0400
	ninKeySelect  = 0x0401
	nifMsg        = 0x00000001
	nifIcon       = 0x00000002
	nifTip        = 0x00000004

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfGrayed    = 0x00000001
	tpmReturn   = 0x0100
	tpmRight    = 0x0002

	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040
	idiApplication = 32512

	gmemMoveable    = 0x0042
	cfUnicodeText   = 13
	cmdOpen         = 1
	cmdCopy         = 2
	cmdQuit         = 3
	shortcutTimeout = 25 * time.Second
)

type point struct{ X, Y int32 }

type msgT struct {
	HWnd     windows.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             windows.Handle
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeoutOrVer    uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     windows.Handle
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procLoadImageW          = user32.NewProc("LoadImageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procOpenClipboard       = user32.NewProc("OpenClipboard")
	procEmptyClipboard      = user32.NewProc("EmptyClipboard")
	procSetClipboardData    = user32.NewProc("SetClipboardData")
	procCloseClipboard      = user32.NewProc("CloseClipboard")
	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procRegisterWindowMsg   = user32.NewProc("RegisterWindowMessageW")
	procSHChangeNotify      = shell32.NewProc("SHChangeNotify")
	procGlobalAlloc         = kernel32.NewProc("GlobalAlloc")
	procGlobalLock          = kernel32.NewProc("GlobalLock")
	procGlobalUnlock        = kernel32.NewProc("GlobalUnlock")

	wndProcCallback   uintptr
	hwnd              windows.Handle
	nid               notifyIconData
	taskbarCreatedMsg uint32
	opts              Options
)

// Run creates the tray icon and pumps the Win32 message loop until the user
// picks Quit (or the icon is removed). It blocks, so callers run it in a
// goroutine.
func Run(o Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	opts = o

	hInst, _, _ := procGetModuleHandleW.Call(0)
	className := windows.StringToUTF16Ptr("MyRouterTrayWindow")
	wndProcCallback = windows.NewCallback(wndProc)
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProcCallback,
		HInstance:     windows.Handle(hInst),
		LpszClassName: className,
	}
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register tray window: %w", err)
	}
	title := windows.StringToUTF16Ptr("MyRouter")
	// A normal top-level window without WS_VISIBLE: it never appears, but unlike
	// a message-only window (HWND_MESSAGE) it can own the foreground window so
	// TrackPopupMenu actually shows the tray menu.
	const wsPopup = 0x80000000
	h, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		wsPopup, 0, 0, 0, 0, 0, 0, hInst, 0)
	if h == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}
	hwnd = windows.Handle(h)
	if err := addIcon(); err != nil {
		procDestroyWindow.Call(uintptr(hwnd))
		return err
	}
	defer removeIcon()
	// Version 4 gives us WM_CONTEXTMENU on right click and separate
	// left/right button events; fall back silently on older shells.
	ver := nid
	ver.UTimeoutOrVer = nimVersion4
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&ver)))
	namePtr, _ := windows.UTF16PtrFromString("TaskbarCreated")
	if r, _, _ := procRegisterWindowMsg.Call(uintptr(unsafe.Pointer(namePtr))); r != 0 {
		taskbarCreatedMsg = uint32(r)
	}
	if opts.OnReady != nil {
		opts.OnReady()
	}

	var msg msgT
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return nil
}

// Stop tears the tray icon down from another goroutine: it posts WM_CLOSE to
// the tray window so the message loop exits, the icon is removed from the
// notification area, and nothing of the tray remains after the process quits.
func Stop() {
	if hwnd == 0 {
		return
	}
	procPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
}

func addIcon() error {
	iconPath := filepath.Join(os.TempDir(), fmt.Sprintf("myrouter-tray-%d.ico", os.Getpid()))
	if err := os.WriteFile(iconPath, iconBytes, 0o600); err != nil {
		return fmt.Errorf("write tray icon: %w", err)
	}
	defer os.Remove(iconPath)

	p, _ := windows.UTF16PtrFromString(iconPath)
	hicon, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
	if hicon == 0 {
		hicon, _, _ = procLoadIconW.Call(0, idiApplication)
	}

	nid = notifyIconData{
		HWnd:             hwnd,
		UID:              1,
		UFlags:           nifMsg | nifIcon | nifTip,
		UCallbackMessage: trayMsg,
		HIcon:            windows.Handle(hicon),
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	copy(nid.SzTip[:], windows.StringToUTF16(opts.Tooltip))
	if r, _, err := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
		return fmt.Errorf("add tray icon: %w", err)
	}
	return nil
}

func removeIcon() {
	if nid.HWnd != 0 {
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	}
}

func wndProc(h windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case trayMsg:
		// NOTIFYICON_VERSION_4: LOWORD(lParam) is the mouse message.
		event := uint32(lParam & 0xffff)
		switch event {
		case wmContextMenu, wmRButtonUp:
			showMenu(h)
		case ninSelect, ninKeySelect:
			// Single left click opens the menu too, so the icon always has a
			// discoverable action even if the user never tries right click.
			showMenu(h)
		case wmLButtonDblClk:
			_ = OpenBrowser(opts.DashboardURL)
		}
		return 0
	case wmCommand:
		handleCommand(uint32(wParam))
		return 0
	case wmClose:
		procDestroyWindow.Call(uintptr(h))
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	if taskbarCreatedMsg != 0 && msg == taskbarCreatedMsg {
		_ = addIcon()
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(h), uintptr(msg), wParam, lParam)
	return r
}

func showMenu(h windows.Handle) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendItem(menu, mfString|mfGrayed, 0, "MyRouter - running")
	appendItem(menu, mfSeparator, 0, "")
	appendItem(menu, mfString, cmdOpen, "Open dashboard")
	appendItem(menu, mfString, cmdCopy, "Copy gateway URL")
	appendItem(menu, mfSeparator, 0, "")
	appendItem(menu, mfString, cmdQuit, "Quit MyRouter")

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(h))
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturn|tpmRight, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(h), 0)
	procPostMessageW.Call(uintptr(h), wmNull, 0, 0)
	if cmd != 0 {
		handleCommand(uint32(cmd))
	}
}

func appendItem(menu uintptr, flags uintptr, id uint32, label string) {
	var p *uint16
	if label != "" {
		p = windows.StringToUTF16Ptr(label)
	}
	procAppendMenuW.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(p)))
}

func handleCommand(id uint32) {
	switch id {
	case cmdOpen:
		_ = OpenBrowser(opts.DashboardURL)
	case cmdCopy:
		_ = copyToClipboard(opts.GatewayURL)
	case cmdQuit:
		if opts.OnQuit != nil {
			opts.OnQuit()
		}
		procDestroyWindow.Call(uintptr(hwnd))
	}
}

func copyToClipboard(text string) error {
	if r, _, _ := procOpenClipboard.Call(uintptr(hwnd)); r == 0 {
		return fmt.Errorf("open clipboard")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()

	utf16 := windows.StringToUTF16(text)
	size := uintptr(len(utf16) * 2)
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("global alloc")
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return fmt.Errorf("global lock")
	}
	dst := unsafe.Slice((*uint16)(unsafe.Pointer(p)), len(utf16))
	copy(dst, utf16)
	procGlobalUnlock.Call(h)
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return fmt.Errorf("set clipboard data")
	}
	return nil
}

// InstallShortcut creates a Desktop shortcut (and optionally a Startup
// shortcut) that launches this executable in tray mode.
func InstallShortcut(exePath string, startup bool) (string, error) {
	exe, err := filepath.Abs(exePath)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(exe)
	q := func(s string) string { return strings.ReplaceAll(s, "'", "''") }

	startupBlock := ""
	if startup {
		startupBlock = `
$startup = [Environment]::GetFolderPath('Startup')
$runPath = Join-Path $startup 'MyRouter.lnk'
if (Test-Path $runPath) { Remove-Item $runPath -Force }
$run = $ws.CreateShortcut($runPath)
$run.TargetPath = $target
$run.Arguments = '-tray'
$run.WorkingDirectory = $workdir
$run.IconLocation = "$target,0"
$run.Description = 'MyRouter - One endpoint. Every model.'
$run.Save()`
	}

	script := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$ws = New-Object -ComObject WScript.Shell
$desktop = [Environment]::GetFolderPath('Desktop')
$target = '%s'
$workdir = '%s'
$path = Join-Path $desktop 'MyRouter.lnk'
if (Test-Path $path) { Remove-Item $path -Force }
$lnk = $ws.CreateShortcut($path)
$lnk.TargetPath = $target
$lnk.Arguments = '-tray -open'
$lnk.WorkingDirectory = $workdir
$lnk.IconLocation = "$target,0"
$lnk.Description = 'MyRouter - One endpoint. Every model.'
$lnk.Save()%s
Write-Output (Join-Path $desktop 'MyRouter.lnk')`, q(exe), q(dir), startupBlock)

	ctx, cancel := context.WithTimeout(context.Background(), shortcutTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create shortcut: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// Explorer caches shortcut icons; ask it to refresh so the new .exe icon
	// shows up immediately instead of a blank/generic page.
	procSHChangeNotify.Call(0x08000000, 0, 0, 0) // SHCNE_ASSOCCHANGED
	// Refresh Explorer's icon cache so the shortcut picks up the new .exe icon
	// immediately (otherwise it keeps showing a blank page icon).
	_ = exec.Command("ie4uinit.exe", "-show").Run()
	_ = exec.Command("ie4uinit.exe", "-ClearIconCache").Run()
	return strings.TrimSpace(string(out)), nil
}
