package native

import (
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Monitor describes one connected display in virtual-screen coordinates.
type Monitor struct {
	Device  string
	Bounds  Rect
	Work    Rect
	Primary bool
}

type monitorInfoEx struct {
	CbSize    uint32
	RcMonitor Rect
	RcWork    Rect
	DwFlags   uint32
	SzDevice  [32]uint16
}

var (
	enumMu      sync.Mutex
	enumResults []Monitor
	// The callback is created once: syscall.NewCallback allocations are
	// permanent, so a per-call callback would leak.
	enumMonitorsCallback = syscall.NewCallback(func(hMonitor, hdc, lprcMonitor, lparam uintptr) uintptr {
		var info monitorInfoEx
		info.CbSize = uint32(unsafe.Sizeof(info))
		if r1, _, _ := procGetMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&info))); r1 != 0 {
			enumResults = append(enumResults, Monitor{
				Device:  windows.UTF16ToString(info.SzDevice[:]),
				Bounds:  info.RcMonitor,
				Work:    info.RcWork,
				Primary: info.DwFlags&monitorInfoPrimary != 0,
			})
		}
		return 1
	})
)

// Monitors returns every connected display in enumeration order. The result
// is empty only if the enumeration API itself fails.
func Monitors() []Monitor {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumResults = nil
	_, _, _ = procEnumDisplayMonitors.Call(0, 0, enumMonitorsCallback, 0)
	out := make([]Monitor, len(enumResults))
	copy(out, enumResults)
	enumResults = nil
	return out
}

// PickMonitor resolves a configured device name against the connected
// displays: exact device match first, then the primary, then the first entry.
// ok is false only when mons is empty (the caller should fall back to
// PrimaryBounds/GetWorkArea).
func PickMonitor(mons []Monitor, device string) (Monitor, bool) {
	if len(mons) == 0 {
		return Monitor{}, false
	}
	device = strings.TrimSpace(device)
	if device != "" {
		for _, m := range mons {
			if strings.EqualFold(m.Device, device) {
				return m, true
			}
		}
	}
	for _, m := range mons {
		if m.Primary {
			return m, true
		}
	}
	return mons[0], true
}

// MonitorWorkForRect returns the current work area of the display containing
// (or nearest to) rect. Unlike SPI_GETWORKAREA, which only reports the
// primary display, this is valid for docks on any monitor.
func MonitorWorkForRect(rect Rect) (Rect, bool) {
	h, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&rect)), monitorDefaultToNearest)
	if h == 0 {
		return Rect{}, false
	}
	var info monitorInfoEx
	info.CbSize = uint32(unsafe.Sizeof(info))
	if r1, _, _ := procGetMonitorInfo.Call(h, uintptr(unsafe.Pointer(&info))); r1 == 0 {
		return Rect{}, false
	}
	return info.RcWork, true
}
