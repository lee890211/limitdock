package ui

import (
	"testing"

	"github.com/lxn/walk"

	"limitdock/internal/native"
)

// Regression for the dual-monitor flicker: the reveal zone must be bounded to
// the dock display on BOTH axes. Before the fix, bottom/top zones ignored X
// and left/right zones ignored Y, so a cursor on a neighboring display at a
// matching coordinate toggled reveal/hide every poll tick.
func TestNearDockEdgeBoundsToDockDisplay(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 2560, Bottom: 1440}
	work := full

	tests := []struct {
		name string
		edge string
		pos  native.Point
		want bool
	}{
		{name: "bottom edge on dock display", edge: "bottom", pos: native.Point{X: 1200, Y: 1438}, want: true},
		{name: "bottom edge on left neighbor same Y", edge: "bottom", pos: native.Point{X: -500, Y: 1438}, want: false},
		{name: "bottom edge on right neighbor same Y", edge: "bottom", pos: native.Point{X: 3000, Y: 1438}, want: false},
		{name: "top edge on dock display", edge: "top", pos: native.Point{X: 1200, Y: 2}, want: true},
		{name: "top edge on neighbor same Y", edge: "top", pos: native.Point{X: -500, Y: 2}, want: false},
		{name: "right edge on dock display", edge: "right", pos: native.Point{X: 2555, Y: 700}, want: true},
		{name: "right edge on stacked neighbor same X", edge: "right", pos: native.Point{X: 2555, Y: 2000}, want: false},
		{name: "left edge on dock display", edge: "left", pos: native.Point{X: 4, Y: 700}, want: true},
		{name: "left edge on stacked neighbor same X", edge: "left", pos: native.Point{X: 4, Y: -300}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nearDockEdge(tt.pos, tt.edge, full, work); got != tt.want {
				t.Fatalf("nearDockEdge(%v, %q) = %v, want %v", tt.pos, tt.edge, got, tt.want)
			}
		})
	}
}

// Monitor rects are half-open: the shared boundary column/row belongs to the
// neighboring display, so it must not count as "near" — otherwise a cursor
// resting on the neighbor's first pixel line re-reveals the dock forever.
func TestNearDockEdgeHalfOpenAtSharedBoundaries(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 2560, Bottom: 1440}
	work := full

	if nearDockEdge(native.Point{X: 2560, Y: 700}, "right", full, work) {
		t.Fatalf("X == full.Right belongs to the neighbor display and must not be near")
	}
	if !nearDockEdge(native.Point{X: 2559, Y: 700}, "right", full, work) {
		t.Fatalf("X == full.Right-1 is the last dock-display column and must be near")
	}
	if nearDockEdge(native.Point{X: 1200, Y: 1440}, "bottom", full, work) {
		t.Fatalf("Y == full.Bottom belongs to the neighbor display and must not be near")
	}
	if !nearDockEdge(native.Point{X: 1200, Y: 1439}, "bottom", full, work) {
		t.Fatalf("Y == full.Bottom-1 is the last dock-display row and must be near")
	}
}

func TestNearDockEdgeCoversTaskbarSeam(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	work := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1032}

	if !nearDockEdge(native.Point{X: 900, Y: 1030}, "bottom", full, work) {
		t.Fatalf("seam just above the taskbar should reveal")
	}
	if !nearDockEdge(native.Point{X: 900, Y: 1060}, "bottom", full, work) {
		t.Fatalf("hovering the taskbar area should reveal")
	}
	if nearDockEdge(native.Point{X: 900, Y: 1020}, "bottom", full, work) {
		t.Fatalf("above the seam should not reveal")
	}
}

// Every point that triggers a reveal must lie inside the keep zone of the
// revealed dock. If this invariant breaks, a single cursor position satisfies
// reveal and hide at once and the dock oscillates at the poll rate — exactly
// the flicker bug this change fixes.
func TestRevealZoneAlwaysInsideKeepZone(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 2560, Bottom: 1440}
	workVariants := map[string]native.Rect{
		"no taskbar":     full,
		"bottom taskbar": {Left: 0, Top: 0, Right: 2560, Bottom: 1392},
		"top taskbar":    {Left: 0, Top: 48, Right: 2560, Bottom: 1440},
	}

	for workName, work := range workVariants {
		for _, edge := range []string{"top", "bottom", "left", "right"} {
			dockWork := dockWorkArea("overlay", edge, full, work)
			rect := dockRect(full, dockWork, edge, isSide(edge), false)
			keep := revealKeepRect(rect, edge, full)
			for y := int32(-20); y <= full.Bottom+20; y += 7 {
				for x := int32(-20); x <= full.Right+20; x += 7 {
					pos := native.Point{X: x, Y: y}
					if !nearDockEdge(pos, edge, full, dockWork) {
						continue
					}
					if !contains(keep, walk.Point{X: int(x), Y: int(y)}) {
						t.Fatalf("%s/%s: reveal point (%d,%d) outside keep zone %#v — this position would flicker", workName, edge, x, y, keep)
					}
				}
			}
		}
	}
}

func TestDisplayBeyondEdge(t *testing.T) {
	primary := native.Rect{Left: 0, Top: 0, Right: 2560, Bottom: 1440}
	below := native.Monitor{Device: `\\.\DISPLAY2`, Bounds: native.Rect{Left: 0, Top: 1440, Right: 2560, Bottom: 2880}}
	right := native.Monitor{Device: `\\.\DISPLAY3`, Bounds: native.Rect{Left: 2560, Top: 200, Right: 4480, Bottom: 1280}}
	self := native.Monitor{Device: `\\.\DISPLAY1`, Bounds: primary, Primary: true}

	stacked := []native.Monitor{self, below}
	sideBySide := []native.Monitor{self, right}
	single := []native.Monitor{self}

	if !displayBeyondEdge(stacked, primary, "bottom") {
		t.Fatalf("display below must make bottom an interior edge")
	}
	if displayBeyondEdge(stacked, primary, "top") || displayBeyondEdge(stacked, primary, "right") || displayBeyondEdge(stacked, primary, "left") {
		t.Fatalf("stacked layout must only mark bottom as interior")
	}
	if !displayBeyondEdge(sideBySide, primary, "right") {
		t.Fatalf("display to the right must make right an interior edge")
	}
	if displayBeyondEdge(sideBySide, primary, "left") {
		t.Fatalf("side-by-side layout must not mark left as interior")
	}
	for _, edge := range []string{"top", "bottom", "left", "right"} {
		if displayBeyondEdge(single, primary, edge) {
			t.Fatalf("single display must have no interior edges (edge %s)", edge)
		}
	}
}

// The dock geometry must respect the display origin so a dock pinned to a
// secondary monitor lands on that monitor, not on the primary.
func TestDockRectHonorsDisplayOrigin(t *testing.T) {
	full := native.Rect{Left: 2560, Top: -200, Right: 4480, Bottom: 880}
	work := native.Rect{Left: 2560, Top: -200, Right: 4480, Bottom: 832}

	bottom := dockRect(full, work, "bottom", false, false)
	if bottom.Left != 2560 || bottom.Right != 4480 {
		t.Fatalf("bottom rect ignores display origin: %#v", bottom)
	}
	if bottom.Bottom > work.Bottom || bottom.Top < work.Top {
		t.Fatalf("bottom rect escapes display work area: %#v", bottom)
	}
	right := dockRect(full, full, "right", true, false)
	if right.Right != full.Right-dockEdgeGap || right.Left != full.Right-dockSideWidth-dockEdgeGap {
		t.Fatalf("right rect ignores display origin: %#v", right)
	}
	hidden := dockRect(full, full, "right", true, true)
	if hidden.Left <= full.Right {
		t.Fatalf("hidden right rect must sit past the display edge: %#v", hidden)
	}
}

func TestDialogBoundsClampsToWorkArea(t *testing.T) {
	work := native.Rect{Left: 2560, Top: 0, Right: 4480, Bottom: 1032}

	centered := dialogBounds(work, 700, 800, -1)
	if centered.X < int(work.Left) || centered.X+centered.Width > int(work.Right) {
		t.Fatalf("centered dialog escapes work area horizontally: %#v", centered)
	}
	if centered.Y < int(work.Top) || centered.Y+centered.Height > int(work.Bottom) {
		t.Fatalf("centered dialog escapes work area vertically: %#v", centered)
	}

	short := native.Rect{Left: 0, Top: 0, Right: 1366, Bottom: 728}
	top := dialogBounds(short, 700, 800, 60)
	if top.Height > int(short.Bottom-short.Top) {
		t.Fatalf("dialog taller than work area must clamp: %#v", top)
	}
	if top.Y+top.Height > int(short.Bottom) || top.Y < int(short.Top) {
		t.Fatalf("dialog bottom must stay inside a short work area: %#v", top)
	}
}

func TestMonitorOptionsSelection(t *testing.T) {
	mons := []native.Monitor{
		{Device: `\\.\DISPLAY1`, Bounds: native.Rect{Right: 2560, Bottom: 1440}, Primary: true},
		{Device: `\\.\DISPLAY2`, Bounds: native.Rect{Left: 2560, Right: 4480, Bottom: 1080}},
	}

	opts, idx := monitorOptions(mons, "")
	if idx != 0 || len(opts) != 3 || opts[0].device != "" {
		t.Fatalf("empty selection should pick Automatic: idx=%d opts=%#v", idx, opts)
	}
	if opts[1].label != "Display 1 — 2560×1440 (primary)" {
		t.Fatalf("unexpected primary label: %q", opts[1].label)
	}

	opts, idx = monitorOptions(mons, `\\.\display2`)
	if idx != 2 || opts[idx].device != `\\.\DISPLAY2` {
		t.Fatalf("device match should be case-insensitive: idx=%d opts=%#v", idx, opts)
	}

	opts, idx = monitorOptions(mons, `\\.\DISPLAY7`)
	if idx != len(opts)-1 || opts[idx].device != `\\.\DISPLAY7` {
		t.Fatalf("disconnected device should be appended and selected: idx=%d opts=%#v", idx, opts)
	}
}
