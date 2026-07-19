package native

import "testing"

func TestPickMonitor(t *testing.T) {
	primary := Monitor{Device: `\\.\DISPLAY1`, Bounds: Rect{Right: 2560, Bottom: 1440}, Primary: true}
	second := Monitor{Device: `\\.\DISPLAY2`, Bounds: Rect{Left: 2560, Right: 4480, Bottom: 1080}}
	mons := []Monitor{second, primary}

	if _, ok := PickMonitor(nil, ""); ok {
		t.Fatalf("empty monitor list must report !ok")
	}
	if m, ok := PickMonitor(mons, ""); !ok || m.Device != primary.Device {
		t.Fatalf("empty device should pick primary, got %#v ok=%v", m, ok)
	}
	if m, ok := PickMonitor(mons, `\\.\display2`); !ok || m.Device != second.Device {
		t.Fatalf("device match should be case-insensitive, got %#v ok=%v", m, ok)
	}
	if m, ok := PickMonitor(mons, `\\.\DISPLAY9`); !ok || m.Device != primary.Device {
		t.Fatalf("unknown device should fall back to primary, got %#v ok=%v", m, ok)
	}
	noPrimary := []Monitor{second}
	if m, ok := PickMonitor(noPrimary, ""); !ok || m.Device != second.Device {
		t.Fatalf("no primary flag should fall back to first entry, got %#v ok=%v", m, ok)
	}
}
