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
