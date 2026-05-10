package ui

import (
	"testing"

	"github.com/lxn/walk"

	"limitdock/internal/native"
)

func TestDockRectVisibleEdgesUseLogicalBarSize(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	work := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}

	tests := []struct {
		name string
		edge string
		side bool
		want native.Rect
	}{
		{name: "bottom", edge: "bottom", want: native.Rect{Left: 0, Top: 942, Right: 1920, Bottom: 1038}},
		{name: "top", edge: "top", want: native.Rect{Left: 0, Top: 2, Right: 1920, Bottom: 98}},
		{name: "left", edge: "left", side: true, want: native.Rect{Left: 2, Top: 0, Right: 352, Bottom: 1040}},
		{name: "right", edge: "right", side: true, want: native.Rect{Left: 1568, Top: 0, Right: 1918, Bottom: 1040}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dockRect(full, work, tt.edge, tt.side, false)
			if got != tt.want {
				t.Fatalf("dockRect() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDockRectHiddenEdgesMoveFullyOffScreen(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	work := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}

	tests := []struct {
		name string
		edge string
		side bool
		want native.Rect
	}{
		{name: "bottom", edge: "bottom", want: native.Rect{Left: 0, Top: 1082, Right: 1920, Bottom: 1178}},
		{name: "top", edge: "top", want: native.Rect{Left: 0, Top: -98, Right: 1920, Bottom: -2}},
		{name: "left", edge: "left", side: true, want: native.Rect{Left: -352, Top: 0, Right: -2, Bottom: 1040}},
		{name: "right", edge: "right", side: true, want: native.Rect{Left: 1922, Top: 0, Right: 2272, Bottom: 1040}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dockRect(full, work, tt.edge, tt.side, true)
			if got != tt.want {
				t.Fatalf("dockRect(hidden) = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestReservedWorkAreaMatchesVisibleDockEdge(t *testing.T) {
	base := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}
	rects := map[string]native.Rect{
		"bottom": {Left: 0, Top: 942, Right: 1920, Bottom: 1038},
		"top":    {Left: 0, Top: 2, Right: 1920, Bottom: 98},
		"left":   {Left: 2, Top: 0, Right: 352, Bottom: 1040},
		"right":  {Left: 1568, Top: 0, Right: 1918, Bottom: 1040},
	}

	want := map[string]native.Rect{
		"bottom": {Left: 0, Top: 0, Right: 1920, Bottom: 942},
		"top":    {Left: 0, Top: 98, Right: 1920, Bottom: 1040},
		"left":   {Left: 352, Top: 0, Right: 1920, Bottom: 1040},
		"right":  {Left: 0, Top: 0, Right: 1568, Bottom: 1040},
	}

	for edge, rect := range rects {
		t.Run(edge, func(t *testing.T) {
			got := reservedWorkArea(base, edge, rect)
			if got != want[edge] {
				t.Fatalf("reservedWorkArea() = %#v, want %#v", got, want[edge])
			}
		})
	}
}

func TestSideOverlayDockAreaIgnoresReservedWorkArea(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	staleWork := native.Rect{Left: 352, Top: 0, Right: 1920, Bottom: 980}
	area := dockWorkArea("overlay", "left", full, staleWork)
	if area != full {
		t.Fatalf("side overlay area = %#v, want full screen %#v", area, full)
	}
	rect := dockRect(full, area, "left", true, false)
	if rect.Left != 2 || rect.Right != 352 {
		t.Fatalf("left overlay rect = %#v, want screen edge width only", rect)
	}
	hidden := dockRect(full, area, "left", true, true)
	if hidden.Right != -2 {
		t.Fatalf("left overlay hidden rect = %#v, want fully offscreen", hidden)
	}
	autoHidden := sideOverlayHiddenRect(full, area, "left")
	if autoHidden.Right >= full.Left {
		t.Fatalf("left overlay auto-hidden rect = %#v, want fully hidden before monitor edge", autoHidden)
	}
}

func TestSideOverlayHiddenRectLeavesNoVisibleStrip(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	work := full
	left := sideOverlayHiddenRect(full, work, "left")
	if left.Right >= full.Left {
		t.Fatalf("left hidden rect leaves visible strip: %#v", left)
	}
	right := sideOverlayHiddenRect(full, work, "right")
	if right.Left <= full.Right {
		t.Fatalf("right hidden rect leaves visible strip: %#v", right)
	}
}

func TestHorizontalOverlayDockAreaPreservesWorkArea(t *testing.T) {
	full := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}
	work := native.Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}
	area := dockWorkArea("overlay", "bottom", full, work)
	if area != work {
		t.Fatalf("horizontal overlay area = %#v, want work area %#v", area, work)
	}
	rect := dockRect(full, area, "bottom", false, false)
	if rect.Bottom > work.Bottom {
		t.Fatalf("bottom overlay rect = %#v, should not overlap menu/taskbar area after %d", rect, work.Bottom)
	}
	if rect.Top != work.Bottom-dockRibbonHeight-dockEdgeGap {
		t.Fatalf("bottom overlay top = %d, want %d", rect.Top, work.Bottom-dockRibbonHeight-dockEdgeGap)
	}
}

func TestRibbonCardWidthFitsFiveCardsWhenAvailable(t *testing.T) {
	const gap = 6
	available := 1764
	width := ribbonCardWidth(available, 5, gap)
	total := width*5 + gap*4
	if total > available {
		t.Fatalf("five cards total width %d exceeds available %d", total, available)
	}
	if width < 340 {
		t.Fatalf("card width %d below minimum", width)
	}
}

func TestAppBarAutoScaleDetectsVirtualizedLeftWorkArea(t *testing.T) {
	desired := native.Rect{Left: 2, Top: 0, Right: 352, Bottom: 1392}
	reported := native.Rect{Left: 235, Top: 0, Right: 2560, Bottom: 1392}
	got := appBarAutoScale("left", reported, desired)
	if got < 1.49 || got > 1.50 {
		t.Fatalf("appBarAutoScale() = %v, want about 1.497", got)
	}
}

func TestSideHeaderStatusStaysOnRight(t *testing.T) {
	bounds := walk.Rectangle{X: 0, Y: 0, Width: 528, Height: 900}
	layout := sideHeaderLayout(bounds, true)
	if layout.status.Right() < bounds.Right()-20 {
		t.Fatalf("status right = %d, want near %d", layout.status.Right(), bounds.Right()-18)
	}
	if overlaps(layout.pin, layout.status) {
		t.Fatalf("status overlaps pin: pin=%#v status=%#v", layout.pin, layout.status)
	}
	if !containsRect(bounds, layout.status) || !containsRect(bounds, layout.statusIcon) || !containsRect(bounds, layout.statusText) {
		t.Fatalf("status layout escapes bounds: %#v", layout)
	}
}

func TestSideLayoutBoundsClampVirtualCanvasWidth(t *testing.T) {
	bounds := walk.Rectangle{X: 0, Y: 0, Width: 2560, Height: 2088}
	got := sideLayoutBoundsForWork(bounds, 1392)
	if got.Width != 525 {
		t.Fatalf("side layout width = %d, want 525", got.Width)
	}
}

func TestSideBandColumnsStayOrderedAndBounded(t *testing.T) {
	rect := walk.Rectangle{X: 0, Y: 42, Width: 525, Height: 78}
	row := sideBandLayout(rect, 71, 18)
	if !containsRect(rect, row.caption) || !containsRect(rect, row.reset) || !containsRect(rect, row.gauge) {
		t.Fatalf("side row escapes card bounds: %#v", row)
	}
	if row.caption.Right() >= row.reset.X || row.reset.Right() >= row.gauge.X {
		t.Fatalf("side row columns overlap or are unordered: %#v", row)
	}
	if row.gauge.Right() < rect.Right()-14 {
		t.Fatalf("gauge right = %d, want near %d", row.gauge.Right(), rect.Right()-12)
	}
}

func TestSideCardHeightFitsFourRows(t *testing.T) {
	height := sideCardHeight(4)
	rect := walk.Rectangle{X: 0, Y: 42, Width: 525, Height: height}
	rowY := rect.Y + sideCardHeaderHeight
	for i := 0; i < 4; i++ {
		if rowY+18 > rect.Bottom()-5 {
			t.Fatalf("row %d escapes side card: rowY=%d rect=%#v", i, rowY, rect)
		}
		rowY += sideCardRowHeight
	}
	if height <= sideCardHeight(2) {
		t.Fatalf("four-row side card height %d should be taller than two-row height %d", height, sideCardHeight(2))
	}
}

func TestThemePickerHitTestingUsesPaintedOptions(t *testing.T) {
	picker := &themePicker{value: "light"}
	picker.selectAt(82, 632)
	if picker.Value() != "night" {
		t.Fatalf("selectAt night icon = %q, want night", picker.Value())
	}
	picker.selectAt(20, 632)
	if picker.Value() != "light" {
		t.Fatalf("selectAt light icon = %q, want light", picker.Value())
	}
}

func TestEdgePickerHitTestingUsesPaintedOptions(t *testing.T) {
	picker := &edgePicker{value: "bottom"}
	picker.selectAt(250, 632)
	if picker.Value() != "right" {
		t.Fatalf("selectAt right icon = %q, want right", picker.Value())
	}
	picker.selectAt(500, 632)
	if picker.Value() != "right" {
		t.Fatalf("click outside painted options changed value to %q", picker.Value())
	}
}

func containsRect(outer, inner walk.Rectangle) bool {
	return inner.X >= outer.X &&
		inner.Y >= outer.Y &&
		inner.Right() <= outer.Right() &&
		inner.Bottom() <= outer.Bottom()
}

func overlaps(a, b walk.Rectangle) bool {
	return a.Width > 0 && a.Height > 0 &&
		b.Width > 0 && b.Height > 0 &&
		a.X < b.Right() && a.Right() > b.X &&
		a.Y < b.Bottom() && a.Bottom() > b.Y
}
