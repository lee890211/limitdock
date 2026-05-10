package ui

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"

	"limitdock/internal/logging"
	"limitdock/internal/native"
	"limitdock/internal/openusage"
	"limitdock/internal/paths"
	"limitdock/internal/provider"
	"limitdock/internal/quota"
	"limitdock/internal/readmodel"
	"limitdock/internal/settings"
)

type Options struct {
	NoDownload     bool
	RefreshSeconds int
}

type App struct {
	paths   paths.Paths
	cfg     settings.Settings
	log     *logging.Logger
	manager *openusage.Manager

	mw      *walk.MainWindow
	surface *walk.CustomWidget
	notify  *walk.NotifyIcon

	fontSmall *walk.Font
	fontBold  *walk.Font
	fontIcon  *walk.Font
	images    map[string]*walk.Bitmap

	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	cards       []quota.Card
	status      string
	visible     bool
	appbar      bool
	baseWork    *native.Rect
	cardHits    []cardHit
	gearHit     walk.Rectangle
	pinHit      walk.Rectangle
	lastCardHit map[string]time.Time
	revealed    bool
}

type cardHit struct {
	rect walk.Rectangle
	card quota.Card
}

func Run(p paths.Paths, opts Options) error {
	if err := paths.Ensure(p); err != nil {
		return err
	}
	log := logging.New(p.Logs)
	cfg, err := settings.Load(p.Settings)
	if err != nil {
		log.Printf("Failed to load settings, using defaults: %v", err)
		cfg = settings.Defaults()
	}
	if opts.RefreshSeconds >= 5 {
		cfg.RefreshSeconds = opts.RefreshSeconds
	}
	app := &App{
		paths:       p,
		cfg:         cfg,
		log:         log,
		visible:     true,
		status:      "Starting",
		images:      map[string]*walk.Bitmap{},
		lastCardHit: map[string]time.Time{},
	}
	app.ctx, app.cancel = context.WithCancel(context.Background())
	app.manager = &openusage.Manager{
		ExePath:    p.OpenUsageExe,
		SocketPath: p.SocketPath,
		Downloads:  p.Downloads,
		ExtractDir: p.OpenUsageDir,
		DaemonPID:  p.DaemonPID,
		OutLog:     p.DaemonOutLog,
		ErrLog:     p.DaemonErrLog,
		Log:        log,
	}
	if err := os.WriteFile(p.AppPID, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		log.Printf("Could not write app pid: %v", err)
	}
	defer os.Remove(p.AppPID)
	defer app.cleanup()

	if err := app.createWindow(); err != nil {
		return err
	}
	go app.bootstrapOpenUsage(opts.NoDownload)
	go app.refreshLoop()
	go app.hoverLoop()
	if code := app.mw.Run(); code != 0 {
		log.Printf("Walk main loop exited with code %d", code)
	}
	return nil
}

func (a *App) createWindow() error {
	var err error
	a.fontSmall, _ = walk.NewFont("Segoe UI", 8, 0)
	a.fontBold, _ = walk.NewFont("Segoe UI", 9, walk.FontBold)
	a.fontIcon, _ = walk.NewFont("Segoe MDL2 Assets", 14, 0)

	a.mw, err = walk.NewMainWindow()
	if err != nil {
		return err
	}
	a.mw.SetName("LimitDock")
	_ = a.mw.SetTitle("LimitDock")
	if brush, err := walk.NewSolidColorBrush(themeBar); err == nil {
		a.mw.SetBackground(brush)
	}
	if icon := a.loadBitmap("LimitDock.png"); icon != nil {
		_ = a.mw.SetIcon(icon)
	}
	a.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		a.cancel()
		a.removeAppBarRegistration()
		if a.notify != nil {
			_ = a.notify.SetVisible(false)
		}
	})

	layout := walk.NewHBoxLayout()
	_ = layout.SetMargins(walk.Margins{})
	_ = layout.SetSpacing(0)
	_ = a.mw.SetLayout(layout)

	a.surface, err = walk.NewCustomWidget(a.mw, 0, a.paint)
	if err != nil {
		return err
	}
	a.surface.SetClearsBackground(true)
	a.surface.SetInvalidatesOnResize(true)
	_ = a.surface.SetAlwaysConsumeSpace(true)
	a.surface.MouseDown().Attach(a.handleMouseDown)
	_ = layout.SetStretchFactor(a.surface, 1)

	a.applyDock()
	if !(a.cfg.DockMode == "overlay" && a.cfg.AutoHide && isSide(a.cfg.DockEdge)) {
		a.mw.Show()
	}
	if err := a.setupTray(); err != nil {
		a.log.Printf("Notify icon setup failed: %v", err)
	}
	return nil
}

func (a *App) setupTray() error {
	ni, err := walk.NewNotifyIcon(a.mw)
	if err != nil {
		return err
	}
	a.notify = ni
	if icon := a.loadBitmap("LimitDock.png"); icon != nil {
		_ = ni.SetIcon(icon)
	}
	_ = ni.SetToolTip("LimitDock")
	a.addTrayAction("Hide Status Bar", func() { a.setStatusVisible(false) })
	a.addTrayAction("Show Status Bar", func() { a.setStatusVisible(true) })
	a.addTrayAction("Settings", func() { a.showSettingsDialog() })
	a.addTrayAction("Exit", func() { _ = a.mw.Close() })
	return ni.SetVisible(true)
}

func (a *App) addTrayAction(text string, fn func()) {
	action := walk.NewAction()
	_ = action.SetText(text)
	action.Triggered().Attach(fn)
	_ = a.notify.ContextMenu().Actions().Add(action)
}

func (a *App) bootstrapOpenUsage(noDownload bool) {
	a.setStatus("Starting OpenUsage")
	if err := a.manager.EnsureBinary(a.ctx, noDownload); err != nil {
		a.log.Printf("OpenUsage binary unavailable: %v", err)
		a.setStatus("OpenUsage unavailable")
		return
	}
	a.manager.EnsureCodexIntegration(a.ctx)
	if err := a.manager.Start(); err != nil {
		a.log.Printf("Failed to launch daemon process: %v", err)
		a.setStatus("Waiting for OpenUsage")
		return
	}
	if a.manager.WaitReady(a.ctx, 12*time.Second) {
		a.setStatus("OpenUsage ready")
		a.refreshOnce()
	} else {
		a.setStatus("Waiting for OpenUsage")
	}
}

func (a *App) refreshLoop() {
	a.refreshOnce()
	ticker := time.NewTicker(time.Duration(max(5, a.cfg.RefreshSeconds)) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.refreshOnce()
		}
	}
}

func (a *App) refreshOnce() {
	if !a.isVisible() {
		return
	}
	model, err := provider.Aggregator{Readers: a.providerReaders(), Log: a.log}.Read(a.ctx)
	if err != nil {
		a.log.Printf("Refresh failed: %v", err)
		a.setStatus("Waiting for OpenUsage")
		return
	}
	cards := quota.Cards(model, a.cfg)
	a.mu.Lock()
	a.cards = cards
	a.mu.Unlock()
	a.setStatus("Updated " + time.Now().Format("15:04:05"))
}

func (a *App) providerReaders() []provider.Reader {
	readers := []provider.Reader{
		provider.OpenUsageReader{
			Client:       readmodel.Client{SocketPath: a.paths.SocketPath},
			SettingsPath: provider.OpenUsageSettingsPath(),
			Log:          a.log,
		},
	}
	if a.cfg.Antigravity.Enabled {
		readers = append(readers, provider.AntigravityReader{
			Config: a.cfg.Antigravity,
			Log:    a.log,
		})
	}
	return readers
}

func (a *App) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	bounds := canvas.BoundsPixels()
	bar := brush(themeBar)
	defer bar.Dispose()
	canvas.FillRectanglePixels(bar, bounds)

	a.mu.Lock()
	cards := append([]quota.Card(nil), a.cards...)
	status := a.status
	a.mu.Unlock()

	side := isSide(a.cfg.DockEdge)
	a.cardHits = nil
	if side {
		a.paintSide(canvas, bounds, cards, status)
	} else {
		a.paintRibbon(canvas, bounds, cards, status)
	}
	return nil
}

func (a *App) paintRibbon(canvas *walk.Canvas, bounds walk.Rectangle, cards []quota.Card, status string) {
	padX := 8
	padTop := 6
	cardH := min(82, max(52, bounds.Height-11))
	rail := walk.Rectangle{X: padX, Y: padTop, Width: 32, Height: cardH}
	a.paintRibbonRail(canvas, rail)

	x := rail.Right() + 6
	statusRect := ribbonStatusRect(bounds, padTop, cardH)
	if statusRect.Width > 0 {
		a.paintRibbonStatus(canvas, statusRect, status)
	}
	right := bounds.Width - 4
	if statusRect.Width > 0 {
		right = statusRect.X - 6
	}
	if len(cards) == 0 {
		a.paintRibbonWaiting(canvas, walk.Rectangle{X: x, Y: padTop, Width: max(340, right-x), Height: cardH})
		return
	}
	gap := 6
	cardW := 500
	for _, card := range cards {
		if x+cardW > right {
			break
		}
		rect := walk.Rectangle{X: x, Y: padTop, Width: cardW, Height: cardH}
		a.paintRibbonCard(canvas, rect, card)
		a.cardHits = append(a.cardHits, cardHit{rect: rect, card: card})
		x += cardW + gap
	}
}

func ribbonStatusRect(bounds walk.Rectangle, top, height int) walk.Rectangle {
	if bounds.Width < 760 {
		return walk.Rectangle{}
	}
	width := 150
	rightPad := 8
	return walk.Rectangle{X: bounds.Width - width - rightPad, Y: top, Width: width, Height: height}
}

func (a *App) paintRibbonRail(canvas *walk.Canvas, rect walk.Rectangle) {
	bg := brush(themeStatusBack)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	a.gearHit = walk.Rectangle{X: rect.X + 2, Y: rect.Y + 4, Width: 28, Height: 28}
	a.pinHit = walk.Rectangle{X: rect.X + 2, Y: rect.Y + 36, Width: 28, Height: 28}
	a.paintRibbonTool(canvas, a.gearHit, "\uE713", themeMuted)
	if a.cfg.DockMode == "overlay" {
		if a.cfg.AutoHide {
			a.paintRibbonTool(canvas, a.pinHit, "\uE77A", themeFore)
		} else {
			a.paintRibbonTool(canvas, a.pinHit, "\uE718", themeAccent)
		}
	} else {
		a.pinHit = walk.Rectangle{}
	}
}

func (a *App) paintRibbonTool(canvas *walk.Canvas, rect walk.Rectangle, text string, color walk.Color) {
	canvas.DrawTextPixels(text, a.fontIcon, color, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
}

func (a *App) paintRibbonWaiting(canvas *walk.Canvas, rect walk.Rectangle) {
	bg := brush(themeStatusBack)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	canvas.DrawTextPixels("LimitDock waiting for OpenUsage", a.fontBold, themeFore, inset(rect, 10, 6), walk.TextLeft|walk.TextTop|walk.TextSingleLine|walk.TextEndEllipsis)
	canvas.DrawTextPixels("Quota rows appear after telemetry refresh", a.fontSmall, themeMuted, walk.Rectangle{X: rect.X + 10, Y: rect.Y + 30, Width: rect.Width - 20, Height: 20}, walk.TextLeft|walk.TextTop|walk.TextSingleLine|walk.TextEndEllipsis)
}

func (a *App) paintRibbonStatus(canvas *walk.Canvas, rect walk.Rectangle, status string) {
	bg := brush(themeStatusBack)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	canvas.DrawTextPixels(ribbonStatusText(status), a.fontBold, themeAccent, inset(rect, 8, 0), walk.TextCenter|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
}

func (a *App) paintRibbonCard(canvas *walk.Canvas, rect walk.Rectangle, card quota.Card) {
	bg := brush(levelColor(card.Level))
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)

	a.paintProviderIcon(canvas, card.Name, walk.Rectangle{X: rect.X + 6, Y: rect.Y + 8, Width: 20, Height: 20})

	fore := levelForeColor(card.Level)
	stackX := rect.X + 36
	stackY := rect.Y + 3
	innerW := max(268, rect.Width-48)
	title := card.Name
	if countMeteredBands(card.Bands) == 0 && strings.TrimSpace(card.Main) != "" {
		title = strings.TrimSpace(card.Name + " " + card.Main)
	}
	canvas.DrawTextPixels(title, a.fontBold, fore, walk.Rectangle{X: stackX, Y: stackY, Width: innerW, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)

	bands := card.Bands
	if len(bands) > a.cfg.GaugeMaxBands {
		bands = bands[:a.cfg.GaugeMaxBands]
	}
	ribbonCols := 1
	if len(bands) > 2 {
		ribbonCols = 2
	}
	ribbonRows := 0
	if len(bands) > 0 {
		ribbonRows = (len(bands) + ribbonCols - 1) / ribbonCols
	}
	cellGap := 6
	cellW := innerW
	if ribbonCols > 1 {
		cellW = max(130, (innerW-cellGap)/2)
	}

	for row := 0; row < ribbonRows; row++ {
		rowY := stackY + 23 + row*21
		for col := 0; col < ribbonCols; col++ {
			idx := row*ribbonCols + col
			if idx >= len(bands) {
				continue
			}
			band := bands[idx]
			cellLeft := stackX + col*(cellW+cellGap)
			a.paintRibbonBand(canvas, cellLeft, rowY, cellW, band, fore)
		}
	}
}

func (a *App) paintRibbonBand(canvas *walk.Canvas, cellLeft, rowY, cellW int, band quota.Band, fore walk.Color) {
	capText := ribbonCaption(band)
	metaText := strings.TrimSpace(band.Reset)
	hasMeta := metaText != ""
	pctW := 38
	capW := max(56, min(96, cellW-86))
	metaW := 0
	showGauge := true
	if hasMeta {
		pctW = 36
		if cellW < 220 {
			showGauge = false
			metaW = max(58, min(76, int(math.Floor(float64(cellW)*0.42))))
			capW = max(46, min(88, cellW-metaW-pctW-6))
		} else {
			metaW = max(64, min(82, int(math.Floor(float64(cellW)*0.25))))
			capW = max(70, min(140, cellW-metaW-pctW-48))
		}
	}
	metaLeft := cellLeft + capW + 2
	pctLeft := metaLeft + metaW + 2
	if !hasMeta {
		pctLeft = cellLeft + capW + 2
	}
	gaugeLeft := pctLeft + pctW + 6
	gaugeW := 0
	if showGauge {
		gaugeW = max(28, cellLeft+cellW-gaugeLeft-2)
	}

	canvas.DrawTextPixels(capText, a.fontSmall, themeMuted, walk.Rectangle{X: cellLeft, Y: rowY, Width: capW, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	if hasMeta {
		canvas.DrawTextPixels(metaText, a.fontSmall, themeMuted, walk.Rectangle{X: metaLeft, Y: rowY, Width: metaW, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	}
	if band.Percent != nil {
		canvas.DrawTextPixels(percentText(band.Percent), a.fontSmall, fore, walk.Rectangle{X: pctLeft, Y: rowY, Width: pctW, Height: 18}, walk.TextRight|walk.TextVCenter|walk.TextSingleLine)
		if showGauge {
			a.paintGaugeLight(canvas, walk.Rectangle{X: gaugeLeft, Y: rowY + 2, Width: gaugeW, Height: 13}, band.Percent)
		}
	} else if band.DisplayDetail != "" {
		canvas.DrawTextPixels(band.DisplayDetail, a.fontSmall, fore, walk.Rectangle{X: pctLeft, Y: rowY, Width: max(44, cellLeft+cellW-pctLeft-2), Height: 17}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	}
}

func (a *App) paintSide(canvas *walk.Canvas, bounds walk.Rectangle, cards []quota.Card, status string) {
	bg := brush(themeSideBack)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, bounds)

	top := walk.Rectangle{X: 0, Y: 0, Width: bounds.Width, Height: 42}
	topBrush := brush(themeSideTop)
	defer topBrush.Dispose()
	canvas.FillRectanglePixels(topBrush, top)
	a.gearHit = walk.Rectangle{X: 10, Y: 8, Width: 28, Height: 26}
	a.pinHit = walk.Rectangle{X: 44, Y: 8, Width: 28, Height: 26}
	a.paintSideTool(canvas, a.gearHit, "\uE713")
	if a.cfg.DockMode == "overlay" {
		if a.cfg.AutoHide {
			a.paintSideTool(canvas, a.pinHit, "\uE77A")
		} else {
			a.paintSideTool(canvas, a.pinHit, "\uE718")
		}
	} else {
		a.pinHit = walk.Rectangle{}
	}
	canvas.DrawTextPixels(status, a.fontSmall, themeSideBlue, walk.Rectangle{X: 82, Y: 10, Width: bounds.Width - 92, Height: 20}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	a.paintHLine(canvas, 41, bounds.Width)

	y := 42
	cardH := 78
	if len(cards) == 0 {
		a.paintSideWaiting(canvas, walk.Rectangle{X: 0, Y: y, Width: bounds.Width, Height: cardH})
		return
	}
	for _, card := range cards {
		if y+cardH > bounds.Height {
			break
		}
		rect := walk.Rectangle{X: 0, Y: y, Width: bounds.Width, Height: cardH}
		a.paintSideCard(canvas, rect, card)
		a.cardHits = append(a.cardHits, cardHit{rect: rect, card: card})
		y += cardH
	}
}

func (a *App) paintSideTool(canvas *walk.Canvas, rect walk.Rectangle, text string) {
	canvas.DrawTextPixels(text, a.fontIcon, themeSideIcon, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
}

func (a *App) paintSideWaiting(canvas *walk.Canvas, rect walk.Rectangle) {
	bg := brush(themeSideCard)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	a.paintHLine(canvas, rect.Y, rect.Width)
	canvas.DrawTextPixels("Waiting for OpenUsage", a.fontBold, themeSideText, walk.Rectangle{X: 16, Y: rect.Y + 12, Width: rect.Width - 24, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	canvas.DrawTextPixels("Quota rows appear after telemetry refresh", a.fontSmall, themeSideMuted, walk.Rectangle{X: 16, Y: rect.Y + 34, Width: rect.Width - 24, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
}

func (a *App) paintSideCard(canvas *walk.Canvas, rect walk.Rectangle, card quota.Card) {
	bg := brush(themeSideCard)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	a.paintHLine(canvas, rect.Y, rect.Width)

	a.paintProviderIcon(canvas, card.Name, walk.Rectangle{X: rect.X + 16, Y: rect.Y + 10, Width: 18, Height: 18})
	canvas.DrawTextPixels(card.Name, a.fontBold, themeSideText, walk.Rectangle{X: rect.X + 45, Y: rect.Y + 8, Width: 180, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)

	rowY := rect.Y + 29
	rowH := 18
	for i, band := range card.Bands {
		if i >= 2 || rowY+rowH > rect.Bottom()-5 {
			break
		}
		canvas.DrawTextPixels(sideCaption(band), a.fontSmall, themeSideText, walk.Rectangle{X: rect.X + 45, Y: rowY, Width: 100, Height: rowH}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(band.Reset, a.fontSmall, themeSideText, walk.Rectangle{X: rect.X + 145, Y: rowY, Width: 70, Height: rowH}, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(percentText(band.Percent), a.fontSmall, themeSideText, walk.Rectangle{X: rect.X + 224, Y: rowY, Width: 38, Height: rowH}, walk.TextRight|walk.TextVCenter|walk.TextSingleLine)
		a.paintGaugeLight(canvas, walk.Rectangle{X: rect.X + 272, Y: rowY + 4, Width: 50, Height: 12}, band.Percent)
		rowY += 20
	}
}

func (a *App) paintProviderIcon(canvas *walk.Canvas, name string, rect walk.Rectangle) {
	switch strings.ToLower(name) {
	case "codex":
		bg := brush(themeCodexIcon)
		defer bg.Dispose()
		canvas.FillEllipsePixels(bg, rect)
		canvas.DrawTextPixels("O", a.fontBold, themeIconFore, inset(rect, 2, 1), walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	case "gemini":
		bg := brush(themeGeminiIcon)
		defer bg.Dispose()
		canvas.FillEllipsePixels(bg, rect)
		canvas.DrawTextPixels("\u2726", a.fontSmall, themeIconFore, inset(rect, 1, 1), walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	default:
		if img := a.loadBitmap(name + ".png"); img != nil {
			canvas.DrawImageStretchedPixels(img, rect)
			return
		}
		bg := brush(themeSideText)
		defer bg.Dispose()
		canvas.FillRectanglePixels(bg, rect)
	}
}

func (a *App) paintHLine(canvas *walk.Canvas, y, width int) {
	pen, _ := walk.NewCosmeticPen(walk.PenSolid, themeSideLine)
	if pen == nil {
		return
	}
	defer pen.Dispose()
	canvas.DrawLinePixels(pen, walk.Point{X: 0, Y: y}, walk.Point{X: width, Y: y})
}

func (a *App) paintGaugeLight(canvas *walk.Canvas, rect walk.Rectangle, pct *float64) {
	track := brush(themeGaugeTrackLight)
	defer track.Dispose()
	canvas.FillRectanglePixels(track, rect)
	pen, _ := walk.NewCosmeticPen(walk.PenSolid, themeGaugeBorder)
	if pen != nil {
		defer pen.Dispose()
		canvas.DrawRectanglePixels(pen, rect)
	}
	if pct == nil {
		return
	}
	w := int(float64(rect.Width-2) * *pct / 100.0)
	if w < 1 && *pct > 0 {
		w = 1
	}
	fill := brush(a.gaugeFillColor(*pct))
	defer fill.Dispose()
	canvas.FillRectanglePixels(fill, walk.Rectangle{X: rect.X + 1, Y: rect.Y + 1, Width: w, Height: rect.Height - 2})
}

func (a *App) gaugeFillColor(remaining float64) walk.Color {
	used := 100 - remaining
	if used >= float64(a.cfg.GaugeCritPercent) {
		return themeGaugeCrit
	}
	if used >= float64(a.cfg.GaugeWarnPercent) {
		return themeGaugeWarn
	}
	return themeGaugeOk
}

func (a *App) handleMouseDown(x, y int, button walk.MouseButton) {
	if button != walk.LeftButton {
		return
	}
	pt := walk.Point{X: x, Y: y}
	if contains(a.gearHit, pt) {
		a.showSettingsDialog()
		return
	}
	if contains(a.pinHit, pt) {
		a.cfg.AutoHide = !a.cfg.AutoHide
		_ = settings.Save(a.paths.Settings, a.cfg)
		a.applyDock()
		a.invalidate()
		return
	}
	for _, hit := range a.cardHits {
		if !contains(hit.rect, pt) {
			continue
		}
		now := time.Now()
		last := a.lastCardHit[hit.card.SnapshotKey]
		a.lastCardHit[hit.card.SnapshotKey] = now
		if now.Sub(last) <= 420*time.Millisecond {
			a.showBandPicker(hit.card)
		}
		return
	}
}

func (a *App) applyDock() {
	if a.mw == nil || a.mw.IsDisposed() {
		return
	}
	a.unregisterAppBar()
	native.SetPopupToolWindow(uintptr(a.mw.Handle()))
	full := native.PrimaryBounds()
	work, err := native.GetWorkArea()
	if err != nil {
		work = full
	}
	rect := dockRect(full, work, a.cfg.DockEdge, isSide(a.cfg.DockEdge), false)
	if a.cfg.DockMode == "reserved" {
		if a.cfg.AutoHide {
			a.cfg.AutoHide = false
			_ = settings.Save(a.paths.Settings, a.cfg)
		}
		a.baseWork = &work
		workScale := native.WindowDpiScale(uintptr(a.mw.Handle()))
		if native.RegisterAppBar(uintptr(a.mw.Handle())) {
			a.appbar = true
			_, _ = native.SetAppBar(uintptr(a.mw.Handle()), a.cfg.DockEdge, scaleRect(rect, workScale))
			if reported, err := native.GetWorkArea(); err == nil {
				if autoScale := appBarAutoScale(a.cfg.DockEdge, reported, rect); autoScale > 1.05 && autoScale < 3 {
					workScale = autoScale
					_, _ = native.SetAppBar(uintptr(a.mw.Handle()), a.cfg.DockEdge, scaleRect(rect, workScale))
				}
			}
		}
		work := work
		switch a.cfg.DockEdge {
		case "top":
			work.Top = rect.Bottom
		case "bottom":
			work.Bottom = rect.Top
		case "left":
			work.Left = rect.Right
		case "right":
			work.Right = rect.Left
		}
		if err := native.SetWorkArea(work); err != nil {
			a.log.Printf("Reserved workarea apply failed: %v", err)
		} else {
			a.log.Printf("Reserved workarea applied edge=%s rect=(%d,%d,%d,%d) base=(%d,%d,%d,%d)", a.cfg.DockEdge, work.Left, work.Top, work.Right, work.Bottom, a.baseWork.Left, a.baseWork.Top, a.baseWork.Right, a.baseWork.Bottom)
		}
	} else if a.cfg.AutoHide && isSide(a.cfg.DockEdge) {
		_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)})
		a.mw.Hide()
		native.SetDockBoundsHidden(uintptr(a.mw.Handle()), rect)
		a.revealed = false
		a.invalidate()
		return
	}
	hiddenHorizontal := false
	if a.cfg.AutoHide {
		rect = dockRect(full, work, a.cfg.DockEdge, isSide(a.cfg.DockEdge), true)
		hiddenHorizontal = true
	}
	_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)})
	native.SetDockBoundsVisible(uintptr(a.mw.Handle()), rect)
	a.revealed = !hiddenHorizontal
	a.invalidate()
}

func (a *App) unregisterAppBar() (native.Rect, bool) {
	a.removeAppBarRegistration()
	if a.baseWork != nil {
		base := *a.baseWork
		if err := native.SetWorkArea(base); err != nil {
			a.log.Printf("Reserved workarea restore failed: %v", err)
		} else {
			a.log.Printf("Reserved workarea restored rect=(%d,%d,%d,%d)", base.Left, base.Top, base.Right, base.Bottom)
		}
		a.baseWork = nil
		return base, true
	}
	return native.Rect{}, false
}

func (a *App) removeAppBarRegistration() {
	if a.appbar && a.mw != nil && !a.mw.IsDisposed() {
		native.RemoveAppBar(uintptr(a.mw.Handle()))
		a.appbar = false
	}
}

func (a *App) setStatusVisible(visible bool) {
	a.mu.Lock()
	a.visible = visible
	a.mu.Unlock()
	if visible {
		a.mw.Show()
		a.applyDock()
		a.refreshOnce()
	} else {
		a.unregisterAppBar()
		full := native.PrimaryBounds()
		work, err := native.GetWorkArea()
		if err != nil {
			work = full
		}
		rect := dockRect(full, work, a.cfg.DockEdge, isSide(a.cfg.DockEdge), false)
		a.mw.Hide()
		native.SetDockBoundsHidden(uintptr(a.mw.Handle()), rect)
	}
}

func (a *App) isVisible() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.visible
}

func (a *App) hoverLoop() {
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if a.cfg.DockMode != "overlay" || !a.cfg.AutoHide || a.mw == nil || a.mw.IsDisposed() {
				continue
			}
			pos := native.CursorPosition()
			full := native.PrimaryBounds()
			work, err := native.GetWorkArea()
			if err != nil {
				work = full
			}
			near := false
			switch a.cfg.DockEdge {
			case "top":
				near = pos.Y >= work.Top && pos.Y <= work.Top+4
			case "bottom":
				near = pos.Y >= work.Bottom-4 && pos.Y <= full.Bottom
			case "left":
				near = pos.X >= full.Left && pos.X <= work.Left+4
			case "right":
				near = pos.X >= work.Right-4 && pos.X <= full.Right
			}
			side := isSide(a.cfg.DockEdge)
			if side {
				rect := dockRect(full, work, a.cfg.DockEdge, true, false)
				if a.revealed {
					extended := walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)}
					if a.cfg.DockEdge == "right" {
						extended.X -= 40
						extended.Width = int(full.Right) - extended.X
					} else {
						extended.X = int(full.Left)
						extended.Width = int(rect.Right) - extended.X + 40
					}
					if !contains(extended, walk.Point{X: int(pos.X), Y: int(pos.Y)}) {
						native.SetDockBoundsHidden(uintptr(a.mw.Handle()), rect)
						a.revealed = false
					}
					continue
				}
				if near {
					native.SetDockBoundsVisible(uintptr(a.mw.Handle()), rect)
					a.revealed = true
					a.invalidate()
				}
				continue
			}
			rect := dockRect(full, work, a.cfg.DockEdge, false, false)
			if a.revealed {
				extended := horizontalRevealRect(rect, a.cfg.DockEdge, full)
				if contains(extended, walk.Point{X: int(pos.X), Y: int(pos.Y)}) {
					continue
				}
				a.mw.Synchronize(func() {
					hidden := dockRect(full, work, a.cfg.DockEdge, false, true)
					_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(hidden.Left), Y: int(hidden.Top), Width: int(hidden.Right - hidden.Left), Height: int(hidden.Bottom - hidden.Top)})
				})
				a.revealed = false
				native.WakeWindow(uintptr(a.mw.Handle()))
				continue
			}
			if near {
				a.mw.Synchronize(func() {
					_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)})
				})
				a.revealed = true
				a.invalidate()
			}
			native.WakeWindow(uintptr(a.mw.Handle()))
		}
	}
}

func horizontalRevealRect(rect native.Rect, edge string, full native.Rect) walk.Rectangle {
	margin := 10
	out := walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)}
	out.X -= margin
	out.Width += margin * 2
	if edge == "top" {
		out.Y = int(full.Top)
		out.Height = int(rect.Bottom-full.Top) + margin
		return out
	}
	out.Y -= margin
	out.Height = int(full.Bottom) - out.Y
	return out
}

func (a *App) showBandPicker(card quota.Card) {
	if len(card.AllBands) == 0 {
		if card.Message != "" {
			walk.MsgBox(a.mw, card.Name, card.Message, walk.MsgBoxIconInformation)
		}
		return
	}
	dlg, err := walk.NewDialogWithFixedSize(a.mw)
	if err != nil {
		return
	}
	defer dlg.Dispose()
	_ = dlg.SetTitle(card.Name + " visible rows")
	_ = dlg.SetClientSize(walk.Size{Width: 420, Height: 360})
	layout := walk.NewVBoxLayout()
	_ = layout.SetMargins(walk.Margins{HNear: 12, VNear: 12, HFar: 12, VFar: 12})
	_ = layout.SetSpacing(7)
	_ = dlg.SetLayout(layout)
	checks := map[string]*walk.CheckBox{}
	hidden := settings.HiddenSet(a.cfg, card.SnapshotKey)
	for _, band := range card.AllBands {
		cb, _ := walk.NewCheckBox(dlg)
		_ = cb.SetText(band.Caption)
		cb.SetChecked(!hidden[band.Key])
		checks[band.Key] = cb
	}
	buttons, _ := walk.NewComposite(dlg)
	bl := walk.NewHBoxLayout()
	_ = buttons.SetLayout(bl)
	save, _ := walk.NewPushButton(buttons)
	_ = save.SetText("OK")
	cancel, _ := walk.NewPushButton(buttons)
	_ = cancel.SetText("Cancel")
	save.Clicked().Attach(func() {
		next := settings.HiddenSet(a.cfg, card.SnapshotKey)
		for k := range next {
			delete(next, k)
		}
		for key, cb := range checks {
			if !cb.Checked() {
				next[key] = true
			}
		}
		if err := settings.Save(a.paths.Settings, a.cfg); err != nil {
			a.log.Printf("Failed to save settings: %v", err)
		}
		dlg.Accept()
		a.refreshOnce()
	})
	cancel.Clicked().Attach(func() { dlg.Cancel() })
	_ = dlg.SetDefaultButton(save)
	_ = dlg.SetCancelButton(cancel)
	dlg.Run()
}

func (a *App) showSettingsDialog() {
	dlg, err := walk.NewDialogWithFixedSize(a.mw)
	if err != nil {
		return
	}
	defer dlg.Dispose()
	_ = dlg.SetTitle("LimitDock Settings")
	_ = dlg.SetClientSize(walk.Size{Width: 620, Height: 520})
	root := walk.NewVBoxLayout()
	_ = root.SetMargins(walk.Margins{HNear: 14, VNear: 14, HFar: 14, VFar: 14})
	_ = root.SetSpacing(8)
	_ = dlg.SetLayout(root)

	mode := combo(dlg, "Display mode", []string{"reserved", "overlay"}, a.cfg.DockMode)
	edge := combo(dlg, "Dock edge", []string{"bottom", "top", "left", "right"}, a.cfg.DockEdge)
	startup := checkbox(dlg, "Start LimitDock when Windows starts", native.StartupEnabled())
	slide := checkbox(dlg, "Slide away when overlay is unpinned", a.cfg.AutoHide)
	refresh := number(dlg, "Refresh seconds", float64(a.cfg.RefreshSeconds), 5, 600)
	maxBands := number(dlg, "Visible rows per card", float64(a.cfg.GaugeMaxBands), 1, 4)
	warn := number(dlg, "Warn when used percent reaches", float64(a.cfg.GaugeWarnPercent), 1, 100)
	crit := number(dlg, "Critical when used percent reaches", float64(a.cfg.GaugeCritPercent), 1, 100)
	agEnabled := checkbox(dlg, "Enable Antigravity custom quota reader", a.cfg.Antigravity.Enabled)
	subtitle := text(dlg, "Antigravity subtitle", a.cfg.Antigravity.Subtitle)
	dataDir := text(dlg, "Antigravity cache directory or endpoint URL", a.cfg.Antigravity.DataDir)
	binary := text(dlg, "Antigravity endpoint URL hint", a.cfg.Antigravity.BinaryPath)

	buttons, _ := walk.NewComposite(dlg)
	bl := walk.NewHBoxLayout()
	_ = buttons.SetLayout(bl)
	save, _ := walk.NewPushButton(buttons)
	_ = save.SetText("Save")
	cancel, _ := walk.NewPushButton(buttons)
	_ = cancel.SetText("Cancel")
	save.Clicked().Attach(func() {
		a.cfg.DockMode = strings.ToLower(strings.TrimSpace(mode.Text()))
		a.cfg.DockEdge = strings.ToLower(strings.TrimSpace(edge.Text()))
		a.cfg.StartWithWindows = startup.Checked()
		a.cfg.AutoHide = slide.Checked()
		a.cfg.RefreshSeconds = int(refresh.Value())
		a.cfg.GaugeMaxBands = int(maxBands.Value())
		a.cfg.GaugeWarnPercent = int(warn.Value())
		a.cfg.GaugeCritPercent = int(crit.Value())
		a.cfg.Antigravity.Enabled = agEnabled.Checked()
		a.cfg.Antigravity.Subtitle = strings.TrimSpace(subtitle.Text())
		a.cfg.Antigravity.DataDir = strings.TrimSpace(dataDir.Text())
		a.cfg.Antigravity.BinaryPath = strings.TrimSpace(binary.Text())
		a.cfg.Normalize()
		if err := settings.Save(a.paths.Settings, a.cfg); err != nil {
			walk.MsgBox(dlg, "LimitDock", err.Error(), walk.MsgBoxIconError)
			return
		}
		if err := native.SetStartupEnabled(a.paths.Root, a.cfg.StartWithWindows); err != nil {
			walk.MsgBox(dlg, "LimitDock", err.Error(), walk.MsgBoxIconWarning)
		}
		dlg.Accept()
		a.applyDock()
		a.refreshOnce()
	})
	cancel.Clicked().Attach(func() { dlg.Cancel() })
	_ = dlg.SetDefaultButton(save)
	_ = dlg.SetCancelButton(cancel)
	dlg.Run()
}

func (a *App) cleanup() {
	a.cancel()
	if base, ok := a.unregisterAppBar(); ok {
		if err := native.ScheduleWorkAreaRestore(base); err != nil {
			a.log.Printf("Reserved delayed workarea restore failed to start: %v", err)
		}
	}
	if a.notify != nil {
		_ = a.notify.SetVisible(false)
		_ = a.notify.Dispose()
	}
	if a.manager != nil {
		a.manager.Stop()
	}
	for _, img := range a.images {
		img.Dispose()
	}
	for _, f := range []*walk.Font{a.fontSmall, a.fontBold, a.fontIcon} {
		if f != nil {
			f.Dispose()
		}
	}
}

func (a *App) setStatus(text string) {
	a.mu.Lock()
	a.status = text
	a.mu.Unlock()
	a.invalidate()
}

func (a *App) invalidate() {
	if a.mw == nil || a.mw.IsDisposed() || a.surface == nil || a.surface.IsDisposed() {
		return
	}
	a.mw.Synchronize(func() {
		if a.surface != nil && !a.surface.IsDisposed() {
			_ = a.surface.Invalidate()
		}
	})
	native.WakeWindow(uintptr(a.mw.Handle()))
}

func (a *App) loadBitmap(name string) *walk.Bitmap {
	base := strings.TrimSuffix(name, ".png") + ".png"
	if img := a.images[base]; img != nil {
		return img
	}
	for _, path := range []string{
		filepath.Join(a.paths.IconDir, base),
		filepath.Join(a.paths.IconDir, "Codex.png"),
	} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		img, err := walk.NewBitmapFromFile(path)
		if err == nil {
			a.images[base] = img
			return img
		}
	}
	return nil
}

func combo(parent walk.Container, label string, values []string, current string) *walk.ComboBox {
	addLabel(parent, label)
	cb, _ := walk.NewComboBox(parent)
	_ = cb.SetModel(values)
	idx := 0
	for i, v := range values {
		if v == current {
			idx = i
			break
		}
	}
	_ = cb.SetCurrentIndex(idx)
	return cb
}

func checkbox(parent walk.Container, label string, checked bool) *walk.CheckBox {
	cb, _ := walk.NewCheckBox(parent)
	_ = cb.SetText(label)
	cb.SetChecked(checked)
	return cb
}

func number(parent walk.Container, label string, value, min, maxv float64) *walk.NumberEdit {
	addLabel(parent, label)
	n, _ := walk.NewNumberEdit(parent)
	_ = n.SetRange(min, maxv)
	_ = n.SetDecimals(0)
	_ = n.SetIncrement(1)
	_ = n.SetValue(value)
	return n
}

func text(parent walk.Container, label, value string) *walk.TextEdit {
	addLabel(parent, label)
	t, _ := walk.NewTextEdit(parent)
	t.SetCompactHeight(true)
	_ = t.SetText(value)
	return t
}

func addLabel(parent walk.Container, text string) {
	l, _ := walk.NewLabel(parent)
	_ = l.SetText(text)
	l.SetTextColor(themeMuted)
}

func dockRect(full native.Rect, work native.Rect, edge string, side bool, hidden bool) native.Rect {
	width := int32(350)
	height := int32(96)
	if side {
		height = work.Bottom - work.Top
	} else {
		width = work.Right - work.Left
	}
	switch edge {
	case "top":
		y := work.Top + 2
		if hidden {
			y = full.Top - height - 2
		}
		return native.Rect{Left: work.Left, Top: y, Right: work.Left + width, Bottom: y + height}
	case "left":
		x := work.Left + 2
		if hidden {
			x = full.Left - width - 2
		}
		return native.Rect{Left: x, Top: work.Top, Right: x + width, Bottom: work.Top + height}
	case "right":
		x := work.Right - width - 2
		if hidden {
			x = full.Right + 2
		}
		return native.Rect{Left: x, Top: work.Top, Right: x + width, Bottom: work.Top + height}
	default:
		y := work.Bottom - height - 2
		if hidden {
			y = full.Bottom + 2
		}
		return native.Rect{Left: work.Left, Top: y, Right: work.Left + width, Bottom: y + height}
	}
}

func scaleRect(rect native.Rect, scale float64) native.Rect {
	if scale <= 0 {
		scale = 1
	}
	return native.Rect{
		Left:   int32(math.Round(float64(rect.Left) * scale)),
		Top:    int32(math.Round(float64(rect.Top) * scale)),
		Right:  int32(math.Round(float64(rect.Right) * scale)),
		Bottom: int32(math.Round(float64(rect.Bottom) * scale)),
	}
}

func appBarAutoScale(edge string, reported native.Rect, desired native.Rect) float64 {
	switch edge {
	case "bottom":
		if reported.Bottom > 0 && reported.Bottom < desired.Top-4 {
			return float64(desired.Top) / float64(reported.Bottom)
		}
	case "top":
		if reported.Top > 0 && reported.Top < desired.Bottom-4 {
			return float64(desired.Bottom) / float64(reported.Top)
		}
	case "left":
		if reported.Left > 0 && reported.Left < desired.Right-4 {
			return float64(desired.Right) / float64(reported.Left)
		}
	case "right":
		if reported.Right > 0 && reported.Right < desired.Left-4 {
			return float64(desired.Left) / float64(reported.Right)
		}
	}
	return 1
}

func brush(c walk.Color) *walk.SolidColorBrush {
	b, _ := walk.NewSolidColorBrush(c)
	return b
}

func levelColor(level string) walk.Color {
	switch level {
	case "critical":
		return themeCriticalBack
	case "warn":
		return themeWarnBack
	case "status":
		return themeStatusBack
	case "info":
		return themeInfoBack
	default:
		return themeOkBack
	}
}

func levelForeColor(level string) walk.Color {
	switch level {
	case "critical":
		return themeCriticalFore
	case "warn":
		return themeWarnFore
	case "status":
		return themeMuted
	default:
		return themeFore
	}
}

func countMeteredBands(bands []quota.Band) int {
	n := 0
	for _, band := range bands {
		if band.Percent != nil {
			n++
		}
	}
	return n
}

func ribbonCaption(band quota.Band) string {
	caption := strings.TrimSpace(band.Caption)
	reset := strings.TrimSpace(band.Reset)
	if reset != "" {
		caption = strings.TrimSpace(strings.TrimSuffix(caption, " "+reset))
	}
	if caption == "" {
		caption = strings.TrimSpace(band.Window)
	}
	return caption
}

func ribbonStatusText(status string) string {
	if strings.TrimSpace(status) == "" {
		return "Updated --:--:--"
	}
	return status
}

func inset(r walk.Rectangle, x, y int) walk.Rectangle {
	return walk.Rectangle{X: r.X + x, Y: r.Y + y, Width: max(0, r.Width-x*2), Height: max(0, r.Height-y*2)}
}

func contains(r walk.Rectangle, p walk.Point) bool {
	return r.Width > 0 && r.Height > 0 && p.X >= r.X && p.X <= r.Right() && p.Y >= r.Y && p.Y <= r.Bottom()
}

func percentText(pct *float64) string {
	if pct == nil {
		return ""
	}
	if math.Abs(*pct-math.Round(*pct)) < 0.05 {
		return fmt.Sprintf("%.0f%%", *pct)
	}
	return fmt.Sprintf("%.1f%%", *pct)
}

func sideCaption(band quota.Band) string {
	caption := strings.TrimSpace(band.Caption)
	reset := strings.TrimSpace(band.Reset)
	if reset != "" {
		caption = strings.TrimSpace(strings.TrimSuffix(caption, " "+reset))
	}
	if caption == "" {
		caption = strings.TrimSpace(band.Window)
	}
	parts := strings.Fields(caption)
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if strings.HasPrefix(last, "23h") || strings.HasPrefix(last, "24h") {
			parts[len(parts)-1] = "1d"
			caption = strings.Join(parts, " ")
		}
	}
	return caption
}

func isSide(edge string) bool {
	return edge == "left" || edge == "right"
}

var (
	themeBar          = walk.RGB(243, 243, 243)
	themeFore         = walk.RGB(32, 32, 32)
	themeMuted        = walk.RGB(96, 96, 96)
	themeAccent       = walk.RGB(0, 99, 177)
	themeOkBack       = walk.RGB(255, 255, 255)
	themeInfoBack     = walk.RGB(232, 232, 232)
	themeStatusBack   = walk.RGB(232, 232, 232)
	themeWarnBack     = walk.RGB(255, 244, 214)
	themeWarnFore     = walk.RGB(94, 66, 0)
	themeCriticalBack = walk.RGB(255, 226, 226)
	themeCriticalFore = walk.RGB(117, 21, 31)

	themeSideBack        = walk.RGB(238, 238, 238)
	themeSideTop         = walk.RGB(232, 232, 232)
	themeSideCard        = walk.RGB(255, 255, 255)
	themeSideLine        = walk.RGB(224, 224, 224)
	themeSideText        = walk.RGB(0, 0, 0)
	themeSideMuted       = walk.RGB(80, 80, 80)
	themeSideBlue        = walk.RGB(0, 99, 177)
	themeSideIcon        = walk.RGB(70, 70, 70)
	themeGaugeTrackLight = walk.RGB(210, 210, 210)
	themeGaugeBorder     = walk.RGB(128, 128, 128)
	themeGaugeOk         = walk.RGB(16, 124, 16)
	themeGaugeWarn       = walk.RGB(196, 128, 0)
	themeGaugeCrit       = walk.RGB(180, 32, 32)
	themeCodexIcon       = walk.RGB(0, 166, 118)
	themeGeminiIcon      = walk.RGB(92, 113, 255)
	themeIconFore        = walk.RGB(255, 255, 255)
)
