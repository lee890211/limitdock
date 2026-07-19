package native

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type Rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type Point struct {
	X int32
	Y int32
}

type Mutex struct {
	handle windows.Handle
}

func AcquireSingleInstance(name string) (*Mutex, bool, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, true, ptr)
	if err != nil {
		return nil, false, err
	}
	if last := windows.GetLastError(); last == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		return nil, false, nil
	}
	return &Mutex{handle: h}, true, nil
}

func (m *Mutex) Close() {
	if m != nil && m.handle != 0 {
		_ = windows.ReleaseMutex(m.handle)
		_ = windows.CloseHandle(m.handle)
		m.handle = 0
	}
}

// OpenURL opens an http/https URL in the user's default browser via
// ShellExecuteW. explorer.exe must not be used for this: it ignores URL query
// strings and falls back to opening a File Explorer window.
func OpenURL(url string) error {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing to open non-http URL %q", url)
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	const swShowNormal = 1
	r1, _, callErr := procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, swShowNormal)
	// ShellExecuteW reports success with a value greater than 32.
	if r1 <= 32 {
		return fmt.Errorf("ShellExecuteW failed (code %d): %v", r1, callErr)
	}
	return nil
}

func PrimaryBounds() Rect {
	w := callInt(user32.NewProc("GetSystemMetrics"), 0)
	h := callInt(user32.NewProc("GetSystemMetrics"), 1)
	return Rect{Right: int32(w), Bottom: int32(h)}
}

func GetWorkArea() (Rect, error) {
	var rect Rect
	r1, _, err := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&rect)), 0)
	if r1 == 0 {
		return Rect{}, err
	}
	return rect, nil
}

func SetWorkArea(rect Rect) error {
	if err := setWorkAreaRaw(ScaleRect(rect, shellScale())); err != nil {
		return err
	}
	reported, err := GetWorkArea()
	if err != nil {
		return nil
	}
	if scale := workAreaCorrectionScale(rect, reported); scale >= 0.5 && scale <= 3 && (scale < 0.95 || scale > 1.05) {
		return setWorkAreaRaw(ScaleRect(rect, scale))
	}
	return nil
}

func setWorkAreaRaw(rect Rect) error {
	r1, _, err := procSystemParametersInfo.Call(spiSetWorkArea, 0, uintptr(unsafe.Pointer(&rect)), 0)
	if r1 == 0 {
		return err
	}
	return nil
}

func ScheduleWorkAreaRestore(rect Rect) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	arg := fmt.Sprintf("%d,%d,%d,%d", rect.Left, rect.Top, rect.Right, rect.Bottom)
	cmd := exec.Command(exe, "--restore-workarea", arg, "--restore-delay-ms", "1200")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func CursorPosition() Point {
	var pt Point
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	return pt
}

func SetPopupToolWindow(hwnd uintptr) {
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlStyle), uintptr(styleDockBase))
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlExStyle), uintptr(exStyleDockVisible))
	_, _, _ = procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpFrameChanged)
}

func SetTopmost(hwnd uintptr) {
	_, _, _ = procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpShowWindow)
}

func SetWindowOpacity(hwnd uintptr, opacityPercent int) {
	if opacityPercent <= 0 || opacityPercent >= 100 {
		style, _, _ := procGetWindowLong.Call(hwnd, uintptr(gwlExStyle))
		_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlExStyle), style&^uintptr(wsExLayered))
		_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpFrameChanged)
		return
	}
	if opacityPercent < 35 {
		opacityPercent = 35
	}
	style, _, _ := procGetWindowLong.Call(hwnd, uintptr(gwlExStyle))
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlExStyle), style|uintptr(wsExLayered))
	alpha := byte((opacityPercent * 255) / 100)
	_, _, _ = procSetLayeredWindowAttributes.Call(hwnd, 0, uintptr(alpha), lwaAlpha)
	_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpFrameChanged)
}

func FocusTopmost(hwnd uintptr) {
	_, _, _ = procShowWindow.Call(hwnd, swShow)
	_, _, _ = procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	_, _, _ = procBringWindowToTop.Call(hwnd)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

func SetDockBoundsVisible(hwnd uintptr, rect Rect) {
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlStyle), uintptr(styleDockBase|wsVisible))
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlExStyle), uintptr(exStyleDockVisible))
	_, _, _ = procSetWindowPos.Call(
		hwnd,
		hwndTopmost,
		uintptr(rect.Left),
		uintptr(rect.Top),
		uintptr(rect.Right-rect.Left),
		uintptr(rect.Bottom-rect.Top),
		swpFrameChanged|swpShowWindow,
	)
	_, _, _ = procShowWindow.Call(hwnd, uintptr(swShow))
	_, _, _ = procBringWindowToTop.Call(hwnd)
}

func SetDockBoundsHidden(hwnd uintptr, rect Rect) {
	_, _, _ = procShowWindow.Call(hwnd, swHide)
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlStyle), uintptr(styleDockBase))
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlExStyle), uintptr(exStyleDockHidden))
	_, _, _ = procSetWindowPos.Call(
		hwnd,
		hwndTopmost,
		uintptr(rect.Left),
		uintptr(rect.Top),
		uintptr(rect.Right-rect.Left),
		uintptr(rect.Bottom-rect.Top),
		swpNoActivate|swpFrameChanged,
	)
}

func WakeWindow(hwnd uintptr) {
	_, _, _ = procPostMessage.Call(hwnd, wmNull, 0, 0)
}

func PostClose(hwnd uintptr) {
	_, _, _ = procPostMessage.Call(hwnd, wmClose, 0, 0)
}

func RegisterAppBar(hwnd uintptr) bool {
	data := appBarData{CbSize: uint32(unsafe.Sizeof(appBarData{})), HWnd: hwnd, CallbackMessage: appbarCallback}
	r1, _, _ := procSHAppBarMessage.Call(abmNew, uintptr(unsafe.Pointer(&data)))
	return r1 != 0
}

func RemoveAppBar(hwnd uintptr) {
	data := appBarData{CbSize: uint32(unsafe.Sizeof(appBarData{})), HWnd: hwnd, CallbackMessage: appbarCallback}
	_, _, _ = procSHAppBarMessage.Call(abmRemove, uintptr(unsafe.Pointer(&data)))
}

func SetAppBar(hwnd uintptr, edge string, requested Rect) (Rect, bool) {
	scale := shellScale()
	data := appBarData{
		CbSize:          uint32(unsafe.Sizeof(appBarData{})),
		HWnd:            hwnd,
		CallbackMessage: appbarCallback,
		Edge:            appBarEdge(edge),
		Rect:            ScaleRect(requested, scale),
	}
	r1, _, _ := procSHAppBarMessage.Call(abmQueryPos, uintptr(unsafe.Pointer(&data)))
	if r1 == 0 {
		return requested, false
	}
	thickW := data.Rect.Right - data.Rect.Left
	thickH := data.Rect.Bottom - data.Rect.Top
	switch strings.ToLower(edge) {
	case "left":
		data.Rect.Right = data.Rect.Left + thickW
	case "right":
		data.Rect.Left = data.Rect.Right - thickW
	case "top":
		data.Rect.Bottom = data.Rect.Top + thickH
	default:
		data.Rect.Top = data.Rect.Bottom - thickH
	}
	r2, _, _ := procSHAppBarMessage.Call(abmSetPos, uintptr(unsafe.Pointer(&data)))
	return unscaleRect(data.Rect, scale), r2 != 0
}

func StartupShortcutPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "LimitDock.lnk")
}

func StartupLaunchPath(root string) string {
	return filepath.Join(root, "LimitDock.exe")
}

func StartupEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRunKey, registry.QUERY_VALUE)
	if err == nil {
		defer key.Close()
		if value, _, err := key.GetStringValue(startupRunName); err == nil && strings.TrimSpace(value) != "" {
			return true
		}
	}
	_, err = os.Stat(StartupShortcutPath())
	return err == nil
}

func SetStartupEnabled(root string, enabled bool) error {
	if !enabled {
		if err := deleteStartupRunValue(); err != nil {
			return err
		}
		return removeLegacyStartupShortcut()
	}
	target := StartupLaunchPath(root)
	key, _, err := registry.CreateKey(registry.CURRENT_USER, startupRunKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open startup run key: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(startupRunName, quoteCommand(target)); err != nil {
		return fmt.Errorf("set startup run value: %w", err)
	}
	return removeLegacyStartupShortcut()
}

func deleteStartupRunValue() error {
	key, err := registry.OpenKey(registry.CURRENT_USER, startupRunKey, registry.SET_VALUE)
	if err != nil {
		if err == windows.ERROR_FILE_NOT_FOUND {
			return nil
		}
		return fmt.Errorf("open startup run key: %w", err)
	}
	defer key.Close()
	if err := key.DeleteValue(startupRunName); err != nil && err != windows.ERROR_FILE_NOT_FOUND {
		return fmt.Errorf("delete startup run value: %w", err)
	}
	return nil
}

func removeLegacyStartupShortcut() error {
	shortcut := StartupShortcutPath()
	if err := os.Remove(shortcut); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func quoteCommand(path string) string {
	return `"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}

func shellScale() float64 {
	monitor, _, _ := procMonitorFromPoint.Call(0, monitorDefaultToPrimary)
	if monitor == 0 {
		return 1
	}
	var dx, dy uint32
	r1, _, _ := procGetDpiForMonitor.Call(monitor, mdTEffectiveDPI, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy)))
	if r1 == 0 && dx >= 72 && dx <= 384 {
		return float64(dx) / 96
	}
	return 1
}

// ScaleRect multiplies every rect component by scale (used for DPI
// correction); it is the single shared implementation for native and ui.
func ScaleRect(rect Rect, scale float64) Rect {
	if scale <= 0 {
		scale = 1
	}
	return Rect{
		Left:   int32(float64(rect.Left)*scale + 0.5),
		Top:    int32(float64(rect.Top)*scale + 0.5),
		Right:  int32(float64(rect.Right)*scale + 0.5),
		Bottom: int32(float64(rect.Bottom)*scale + 0.5),
	}
}

func unscaleRect(rect Rect, scale float64) Rect {
	if scale <= 0 {
		scale = 1
	}
	return Rect{
		Left:   int32(float64(rect.Left)/scale + 0.5),
		Top:    int32(float64(rect.Top)/scale + 0.5),
		Right:  int32(float64(rect.Right)/scale + 0.5),
		Bottom: int32(float64(rect.Bottom)/scale + 0.5),
	}
}

func workAreaCorrectionScale(desired Rect, reported Rect) float64 {
	var total float64
	var count float64
	for _, pair := range [][2]int32{
		{desired.Left, reported.Left},
		{desired.Top, reported.Top},
		{desired.Right, reported.Right},
		{desired.Bottom, reported.Bottom},
	} {
		want := pair[0]
		got := pair[1]
		if want == 0 || got == 0 {
			continue
		}
		if abs32(want-got) <= 4 {
			continue
		}
		total += float64(want) / float64(got)
		count++
	}
	if count == 0 {
		return 1
	}
	return total / count
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func appBarEdge(edge string) uint32 {
	switch strings.ToLower(edge) {
	case "left":
		return abeLeft
	case "top":
		return abeTop
	case "right":
		return abeRight
	default:
		return abeBottom
	}
}

func callInt(proc *windows.LazyProc, args ...uintptr) int {
	r1, _, _ := proc.Call(args...)
	return int(r1)
}

type appBarData struct {
	CbSize          uint32
	HWnd            uintptr
	CallbackMessage uint32
	Edge            uint32
	Rect            Rect
	LParam          uintptr
}

var (
	user32                         = windows.NewLazySystemDLL("user32.dll")
	shell32                        = windows.NewLazySystemDLL("shell32.dll")
	procSystemParametersInfo       = user32.NewProc("SystemParametersInfoW")
	procGetCursorPos               = user32.NewProc("GetCursorPos")
	procGetWindowLong              = user32.NewProc("GetWindowLongW")
	procSetWindowLong              = user32.NewProc("SetWindowLongW")
	procSetWindowPos               = user32.NewProc("SetWindowPos")
	procBringWindowToTop           = user32.NewProc("BringWindowToTop")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procPostMessage                = user32.NewProc("PostMessageW")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procMonitorFromPoint           = user32.NewProc("MonitorFromPoint")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procSHAppBarMessage            = shell32.NewProc("SHAppBarMessage")
	procShellExecute               = shell32.NewProc("ShellExecuteW")
	shcore                         = windows.NewLazySystemDLL("shcore.dll")
	procGetDpiForMonitor           = shcore.NewProc("GetDpiForMonitor")
)

const (
	spiSetWorkArea  = 0x002F
	spiGetWorkArea  = 0x0030
	mdTEffectiveDPI = 0

	gwlStyle   = ^uintptr(15)
	gwlExStyle = ^uintptr(19)

	wsVisible          = 0x10000000
	wsPopup            = 0x80000000
	wsClipSiblings     = 0x04000000
	wsClipChildren     = 0x02000000
	wsExTopmost        = 0x00000008
	wsExToolWindow     = 0x00000080
	wsExLayered        = 0x00080000
	wsExNoActivate     = 0x08000000
	styleDockBase      = wsPopup | wsClipSiblings | wsClipChildren
	exStyleDockVisible = wsExTopmost | wsExToolWindow
	exStyleDockHidden  = wsExTopmost | wsExToolWindow | wsExNoActivate
	wmNull             = 0x0000
	wmClose            = 0x0010
	lwaAlpha           = 0x00000002
	swHide             = 0x0000
	swShow             = 0x0005

	hwndTopmost = ^uintptr(0)

	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoActivate   = 0x0010
	swpShowWindow   = 0x0040
	swpFrameChanged = 0x0020

	abmNew      = 0x00000000
	abmRemove   = 0x00000001
	abmQueryPos = 0x00000002
	abmSetPos   = 0x00000003

	abeLeft   = 0
	abeTop    = 1
	abeRight  = 2
	abeBottom = 3

	monitorDefaultToPrimary = 1
	appbarCallback          = 0x8001
	startupRunKey           = `Software\Microsoft\Windows\CurrentVersion\Run`
	startupRunName          = "LimitDock"
)
