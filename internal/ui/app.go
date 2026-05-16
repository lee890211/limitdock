package ui

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/lxn/walk"

	openusage "limitdock/internal/connector/openusage"
	"limitdock/internal/logging"
	"limitdock/internal/native"
	"limitdock/internal/paths"
	"limitdock/internal/provider"
	"limitdock/internal/quota"
	"limitdock/internal/readmodel"
	"limitdock/internal/settings"
)

const (
	appName              = "LimitDock"
	statusStarting       = "Starting"
	statusStartingOU     = "Starting OpenUsage"
	statusOUReady        = "OpenUsage ready"
	statusOUUnavailable  = "OpenUsage unavailable"
	statusWaitingOU      = "Waiting for OpenUsage"
	statusRefreshing     = "Refreshing"
	statusUpdatedPrefix  = "Updated "
	statusUpdatedEmpty   = "Updated --:--:--"
	statusReady          = "Ready"
	statusUnavailable    = "Unavailable"
	statusWaiting        = "Waiting"
	textOpenUsage        = "OpenUsage"
	textWaitingTitle     = "LimitDock waiting for OpenUsage"
	textWaitingSideTitle = "Waiting for OpenUsage"
	textWaitingDetail    = "Quota rows appear after telemetry refresh"
	trayHideStatus       = "Hide Status Bar"
	trayShowStatus       = "Show Status Bar"
	traySettings         = "Settings"
	trayExit             = "Exit"
	settingsTitle        = "LimitDock Settings"
	settingsTheme        = "Theme"
	settingsDisplayMode  = "Display mode"
	settingsPosition     = "Position"
	settingsStartup      = "Start LimitDock when Windows starts"
	settingsAutoSlide    = "Auto slide in overlay mode"
	settingsOpacity      = "Overlay opacity %"
	settingsRefresh      = "Refresh seconds"
	settingsGaugeBands   = "Visible rows per card (max 4)"
	settingsGaugeWarn    = "Warn when used %"
	settingsGaugeCrit    = "Critical when used %"
	settingsDiagnostics  = "OpenUsage / logs"
	settingsOpenOU       = "OpenUsage settings"
	settingsOpenOUFolder = "OpenUsage folder"
	settingsOpenLogs     = "Logs folder"
	settingsOpenAppLog   = "LimitDock log"
	settingsCopyDiag     = "Copy diagnostics"
	settingsOK           = "OK"
	settingsSave         = "Save"
	settingsCancel       = "Cancel"
	glyphSettings        = "\uE713"
	glyphPinned          = "\uE718"
	glyphUnpinned        = "\uE77A"
	glyphRefresh         = "\uE72C"
	glyphClock           = "\uE121"
	dockSideWidth        = int32(350)
	dockRibbonHeight     = int32(96)
	dockEdgeGap          = int32(2)
	dockRevealStrip      = int32(8)
	sideCardHeaderHeight = 29
	sideCardRowHeight    = 20
	sideCardBottomPad    = 9
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

	mw       *walk.MainWindow
	surface  *walk.CustomWidget
	notify   *walk.NotifyIcon
	trayHide *walk.Action
	trayShow *walk.Action

	fontSmall *walk.Font
	fontBold  *walk.Font
	fontIcon  *walk.Font
	images    map[string]*walk.Bitmap

	ctx          context.Context
	cancel       context.CancelFunc
	shutdownOnce sync.Once
	shutdownMu   sync.Mutex
	shutdownDone bool

	mu          sync.Mutex
	cards       []quota.Card
	status      string
	visible     bool
	appbar      bool
	baseWork    *native.Rect
	cardHits    []cardHit
	gearHit     walk.Rectangle
	pinHit      walk.Rectangle
	statusHit   walk.Rectangle
	lastCardHit map[string]time.Time
	lastMouseAt time.Time
	lastMousePt walk.Point
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
	applyTheme(cfg.Theme)
	app := &App{
		paths:       p,
		cfg:         cfg,
		log:         log,
		visible:     true,
		status:      statusStarting,
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
	a.mw.SetName(appName)
	_ = a.mw.SetTitle(appName)
	if brush, err := walk.NewSolidColorBrush(themeBar); err == nil {
		a.mw.SetBackground(brush)
	}
	if icon := a.loadBitmap("LimitDock.png"); icon != nil {
		_ = a.mw.SetIcon(icon)
	}
	a.mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if !a.isShutdownDone() {
			*canceled = true
			a.beginShutdown()
		}
	})

	layout := walk.NewHBoxLayout()
	_ = layout.SetMargins(walk.Margins{})
	_ = layout.SetSpacing(0)
	_ = a.mw.SetLayout(layout)

	a.surface, err = walk.NewCustomWidgetPixels(a.mw, 0, a.paint)
	if err != nil {
		return err
	}
	a.surface.SetClearsBackground(true)
	a.surface.SetInvalidatesOnResize(true)
	_ = a.surface.SetAlwaysConsumeSpace(true)
	a.surface.MouseUp().Attach(a.handleMouseDown)
	a.mw.MouseUp().Attach(a.handleMouseDown)
	_ = layout.SetStretchFactor(a.surface, 1)

	if a.cfg.DockMode == "overlay" && a.cfg.AutoHide && isSide(a.cfg.DockEdge) {
		a.applyDock()
	} else {
		a.mw.Show()
		a.applyDock()
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
	_ = ni.SetToolTip(appName)
	a.trayHide = a.addTrayAction(trayHideStatus, func() { a.setStatusVisible(false) })
	a.trayShow = a.addTrayAction(trayShowStatus, func() { a.setStatusVisible(true) })
	a.addTrayAction(traySettings, func() { a.showSettingsDialog() })
	a.addTrayAction(trayExit, func() {
		a.beginShutdown()
	})
	a.refreshTrayVisibilityActions()
	return ni.SetVisible(true)
}

func (a *App) addTrayAction(text string, fn func()) *walk.Action {
	action := walk.NewAction()
	_ = action.SetText(text)
	action.Triggered().Attach(fn)
	_ = a.notify.ContextMenu().Actions().Add(action)
	return action
}

func (a *App) refreshTrayVisibilityActions() {
	visible := a.isVisible()
	if a.trayHide != nil {
		_ = a.trayHide.SetVisible(visible)
	}
	if a.trayShow != nil {
		_ = a.trayShow.SetVisible(!visible)
	}
}

func (a *App) bootstrapOpenUsage(noDownload bool) {
	a.setStatus(statusStartingOU)
	if err := a.manager.EnsureBinary(a.ctx, noDownload); err != nil {
		if a.ctx.Err() != nil {
			return
		}
		a.log.Printf("OpenUsage binary unavailable: %v", err)
		a.setStatus(statusOUUnavailable)
		return
	}
	if a.ctx.Err() != nil {
		return
	}
	if err := a.manager.Start(); err != nil {
		if a.ctx.Err() != nil {
			return
		}
		a.log.Printf("Failed to launch daemon process: %v", err)
		a.setStatus(statusWaitingOU)
		return
	}
	if a.manager.WaitReady(a.ctx, 12*time.Second) {
		a.setStatus(statusOUReady)
		a.refreshOnce()
	} else if a.ctx.Err() == nil {
		a.setStatus(statusWaitingOU)
	}
}

func (a *App) refreshLoop() {
	a.refreshOnce()
	for {
		timer := time.NewTimer(time.Duration(max(5, a.cfg.RefreshSeconds)) * time.Second)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
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
		a.setStatus(statusWaitingOU)
		return
	}
	cards := quota.Cards(model, a.cfg)
	a.mu.Lock()
	a.cards = cards
	a.mu.Unlock()
	a.setStatus(statusUpdatedPrefix + time.Now().Format("15:04:05"))
}

func (a *App) providerReaders() []provider.Reader {
	readers := []provider.Reader{
		openusage.Reader{
			Client: readmodel.Client{SocketPath: a.paths.SocketPath},
		},
		provider.CodexReader{
			Log: a.log,
		},
	}
	readers = append(readers, provider.AntigravityReader{Log: a.log})
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
	a.statusHit = walk.Rectangle{}
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
	cardW := ribbonCardWidth(right-x, len(cards), gap)
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
	width := 98
	rightPad := 8
	return walk.Rectangle{X: bounds.Width - width - rightPad, Y: top, Width: width, Height: height}
}

func (a *App) paintRibbonRail(canvas *walk.Canvas, rect walk.Rectangle) {
	bg := brush(themeStatusBack)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	a.gearHit = walk.Rectangle{X: rect.X + 2, Y: rect.Y + 4, Width: 28, Height: 28}
	a.pinHit = walk.Rectangle{X: rect.X + 2, Y: rect.Y + 36, Width: 28, Height: 28}
	a.paintRibbonTool(canvas, a.gearHit, glyphSettings, themeMuted)
	if a.cfg.DockMode == "overlay" {
		if a.cfg.AutoHide {
			a.paintRibbonTool(canvas, a.pinHit, glyphUnpinned, themeFore)
		} else {
			a.paintRibbonTool(canvas, a.pinHit, glyphPinned, themeAccent)
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
	canvas.DrawTextPixels(textWaitingTitle, a.fontBold, themeFore, inset(rect, 10, 6), walk.TextLeft|walk.TextTop|walk.TextSingleLine|walk.TextEndEllipsis)
	canvas.DrawTextPixels(textWaitingDetail, a.fontSmall, themeMuted, walk.Rectangle{X: rect.X + 10, Y: rect.Y + 30, Width: rect.Width - 20, Height: 20}, walk.TextLeft|walk.TextTop|walk.TextSingleLine|walk.TextEndEllipsis)
}

func (a *App) paintRibbonStatus(canvas *walk.Canvas, rect walk.Rectangle, status string) {
	a.statusHit = rect
	bg := brush(themeStatusBack)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	label, value := ribbonStatusParts(status)
	iconRect := walk.Rectangle{X: rect.X + (rect.Width-18)/2, Y: rect.Y + 8, Width: 18, Height: 18}
	canvas.DrawTextPixels(glyphRefresh, a.fontIcon, themeAccent, iconRect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	canvas.DrawTextPixels(label, a.fontBold, themeAccent, walk.Rectangle{X: rect.X + 6, Y: rect.Y + 27, Width: max(0, rect.Width-12), Height: 18}, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	if value != "" {
		canvas.DrawTextPixels(value, a.fontSmall, themeAccent, walk.Rectangle{X: rect.X + 6, Y: rect.Y + 45, Width: max(0, rect.Width-12), Height: 18}, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	}
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
	metaText := resetDisplayText(band.Reset)
	hasMeta := metaText != ""
	pctW := 36
	capW := max(56, min(96, cellW-74))
	metaW := 0
	showGauge := true
	if hasMeta {
		if cellW < 220 {
			showGauge = false
			metaW = max(58, min(76, int(math.Floor(float64(cellW)*0.42))))
			capW = max(46, min(88, cellW-metaW-pctW-6))
		} else {
			metaW = max(64, min(82, int(math.Floor(float64(cellW)*0.25))))
			capW = max(70, min(140, cellW-metaW-58))
		}
	}
	metaLeft := cellLeft + capW + 2
	pctLeft := metaLeft + metaW + 2
	if !hasMeta {
		pctLeft = cellLeft + capW + 2
	}
	gaugeLeft := pctLeft
	gaugeW := 0
	if showGauge {
		gaugeW = max(40, cellLeft+cellW-gaugeLeft-2)
	}

	canvas.DrawTextPixels(capText, a.fontSmall, themeMuted, walk.Rectangle{X: cellLeft, Y: rowY, Width: capW, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	if hasMeta {
		a.paintResetText(canvas, walk.Rectangle{X: metaLeft, Y: rowY, Width: metaW, Height: 18}, metaText, themeMuted)
	}
	if band.Percent != nil {
		if showGauge {
			a.paintGaugeLight(canvas, walk.Rectangle{X: gaugeLeft, Y: rowY + 2, Width: gaugeW, Height: 13}, band.Percent, themeFore)
		} else {
			canvas.DrawTextPixels(percentText(band.Percent), a.fontSmall, fore, walk.Rectangle{X: pctLeft, Y: rowY, Width: pctW, Height: 18}, walk.TextRight|walk.TextVCenter|walk.TextSingleLine)
		}
	} else if band.DisplayDetail != "" {
		canvas.DrawTextPixels(band.DisplayDetail, a.fontSmall, fore, walk.Rectangle{X: pctLeft, Y: rowY, Width: max(44, cellLeft+cellW-pctLeft-2), Height: 17}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	}
}

func (a *App) paintSide(canvas *walk.Canvas, bounds walk.Rectangle, cards []quota.Card, status string) {
	bg := brush(themeSideBack)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, bounds)
	if a.cfg.DockMode == "overlay" && a.cfg.AutoHide && !a.revealed {
		a.gearHit = walk.Rectangle{}
		a.pinHit = walk.Rectangle{}
		a.statusHit = walk.Rectangle{}
		return
	}

	layoutBounds := a.sideLayoutBounds(bounds)
	top := walk.Rectangle{X: 0, Y: 0, Width: bounds.Width, Height: 42}
	topBrush := brush(themeSideTop)
	defer topBrush.Dispose()
	canvas.FillRectanglePixels(topBrush, top)
	header := sideHeaderLayout(layoutBounds, a.cfg.DockMode == "overlay")
	a.gearHit = header.gear
	a.pinHit = header.pin
	a.paintSideTool(canvas, a.gearHit, glyphSettings)
	if a.cfg.DockMode == "overlay" {
		if a.cfg.AutoHide {
			a.paintSideTool(canvas, a.pinHit, glyphUnpinned)
		} else {
			a.paintSideTool(canvas, a.pinHit, glyphPinned)
		}
	} else {
		a.pinHit = walk.Rectangle{}
	}
	a.statusHit = header.status
	canvas.DrawTextPixels(glyphRefresh, a.fontIcon, themeSideBlue, header.statusIcon, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	label, value := ribbonStatusParts(status)
	if value == "" {
		canvas.DrawTextPixels(label, a.fontSmall, themeSideBlue, header.statusText, walk.TextRight|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	} else {
		canvas.DrawTextPixels(label, a.fontSmall, themeSideBlue, walk.Rectangle{X: header.statusText.X, Y: header.statusText.Y - 1, Width: header.statusText.Width, Height: 13}, walk.TextRight|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
		canvas.DrawTextPixels(value, a.fontSmall, themeSideBlue, walk.Rectangle{X: header.statusText.X, Y: header.statusText.Y + 13, Width: header.statusText.Width, Height: 13}, walk.TextRight|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	}
	a.paintHLine(canvas, 41, layoutBounds.Width)

	y := 42
	cardH := sideCardHeight(a.cfg.GaugeMaxBands)
	if len(cards) == 0 {
		a.paintSideWaiting(canvas, walk.Rectangle{X: 0, Y: y, Width: layoutBounds.Width, Height: cardH})
		return
	}
	for _, card := range cards {
		cardH = sideCardHeight(len(card.Bands))
		if y+cardH > bounds.Height {
			break
		}
		rect := walk.Rectangle{X: 0, Y: y, Width: layoutBounds.Width, Height: cardH}
		a.paintSideCard(canvas, rect, card)
		a.cardHits = append(a.cardHits, cardHit{rect: rect, card: card})
		y += cardH
	}
}

func (a *App) sideLayoutBounds(bounds walk.Rectangle) walk.Rectangle {
	work, err := native.GetWorkArea()
	if err == nil && bounds.Height > 0 {
		return sideLayoutBoundsForWork(bounds, work.Bottom-work.Top)
	}
	return sideLayoutBoundsForWork(bounds, 0)
}

func sideLayoutBoundsForWork(bounds walk.Rectangle, workHeight int32) walk.Rectangle {
	if workHeight > 0 && bounds.Height > 0 {
		scale := float64(bounds.Height) / float64(workHeight)
		if scale >= 0.75 && scale <= 3 {
			width := int(math.Round(float64(dockSideWidth) * scale))
			if width > 0 && width < bounds.Width {
				bounds.Width = width
				return bounds
			}
		}
	}
	maxSideWidth := int(dockSideWidth) * 2
	if bounds.Width > maxSideWidth {
		bounds.Width = maxSideWidth
	}
	return bounds
}

func (a *App) paintSideTool(canvas *walk.Canvas, rect walk.Rectangle, text string) {
	canvas.DrawTextPixels(text, a.fontIcon, themeSideIcon, rect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
}

type sideHeaderRects struct {
	gear       walk.Rectangle
	pin        walk.Rectangle
	status     walk.Rectangle
	statusIcon walk.Rectangle
	statusText walk.Rectangle
}

func sideHeaderLayout(bounds walk.Rectangle, overlay bool) sideHeaderRects {
	gear := walk.Rectangle{X: 10, Y: 8, Width: 28, Height: 26}
	pin := walk.Rectangle{}
	toolRight := gear.Right()
	if overlay {
		pin = walk.Rectangle{X: 44, Y: 8, Width: 28, Height: 26}
		toolRight = pin.Right()
	}
	rightPad := 18
	statusW := min(160, max(126, bounds.Width-toolRight-rightPad-16))
	status := walk.Rectangle{X: max(toolRight+8, bounds.Right()-statusW-rightPad), Y: 8, Width: statusW, Height: 26}
	if status.Right() > bounds.Right()-rightPad {
		status.X = bounds.Right() - status.Width - rightPad
	}
	icon := walk.Rectangle{X: status.X, Y: status.Y, Width: 20, Height: 24}
	text := walk.Rectangle{X: icon.Right() + 4, Y: status.Y + 1, Width: max(0, status.Right()-icon.Right()-4), Height: 22}
	return sideHeaderRects{gear: gear, pin: pin, status: status, statusIcon: icon, statusText: text}
}

func (a *App) paintSideWaiting(canvas *walk.Canvas, rect walk.Rectangle) {
	bg := brush(themeSideCard)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	a.paintHLine(canvas, rect.Y, rect.Width)
	canvas.DrawTextPixels(textWaitingSideTitle, a.fontBold, themeSideText, walk.Rectangle{X: 16, Y: rect.Y + 12, Width: rect.Width - 24, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	canvas.DrawTextPixels(textWaitingDetail, a.fontSmall, themeSideMuted, walk.Rectangle{X: 16, Y: rect.Y + 34, Width: rect.Width - 24, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
}

func (a *App) paintSideCard(canvas *walk.Canvas, rect walk.Rectangle, card quota.Card) {
	bg := brush(themeSideCard)
	defer bg.Dispose()
	canvas.FillRectanglePixels(bg, rect)
	a.paintHLine(canvas, rect.Y, rect.Width)

	a.paintProviderIcon(canvas, card.Name, walk.Rectangle{X: rect.X + 16, Y: rect.Y + 10, Width: 18, Height: 18})
	canvas.DrawTextPixels(card.Name, a.fontBold, themeSideText, walk.Rectangle{X: rect.X + 45, Y: rect.Y + 8, Width: 180, Height: 18}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)

	rowY := rect.Y + sideCardHeaderHeight
	rowH := 18
	for i, band := range card.Bands {
		if i >= 4 || rowY+rowH > rect.Bottom()-5 {
			break
		}
		row := sideBandLayout(rect, rowY, rowH)
		canvas.DrawTextPixels(sideCaption(band), a.fontSmall, themeSideText, row.caption, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
		a.paintResetText(canvas, row.reset, band.Reset, themeSideText)
		if band.Percent != nil {
			a.paintGaugeLight(canvas, row.gauge, band.Percent, themeSideText)
		}
		rowY += sideCardRowHeight
	}
}

func sideCardHeight(rowCount int) int {
	rows := max(1, min(4, rowCount))
	return sideCardHeaderHeight + rows*sideCardRowHeight + sideCardBottomPad
}

type sideBandRects struct {
	caption walk.Rectangle
	reset   walk.Rectangle
	gauge   walk.Rectangle
}

func sideBandLayout(rect walk.Rectangle, rowY, rowH int) sideBandRects {
	rightPad := 12
	gaugeW := min(118, max(72, rect.Width/3))
	gauge := walk.Rectangle{X: rect.Right() - rightPad - gaugeW, Y: rowY + 3, Width: gaugeW, Height: 14}
	resetW := 56
	reset := walk.Rectangle{X: gauge.X - resetW - 8, Y: rowY, Width: resetW, Height: rowH}
	captionX := rect.X + 45
	caption := walk.Rectangle{X: captionX, Y: rowY, Width: max(40, reset.X-captionX-8), Height: rowH}
	return sideBandRects{caption: caption, reset: reset, gauge: gauge}
}

func (a *App) paintProviderIcon(canvas *walk.Canvas, name string, rect walk.Rectangle) {
	if img := a.loadBitmap(name + ".png"); img != nil {
		canvas.DrawImageStretchedPixels(img, rect)
		return
	}
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

func (a *App) paintResetText(canvas *walk.Canvas, rect walk.Rectangle, reset string, color walk.Color) {
	text := resetDisplayText(reset)
	if text == "" || rect.Width <= 0 {
		return
	}
	if rect.Width < 30 {
		canvas.DrawTextPixels(text, a.fontSmall, color, rect, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
		return
	}
	iconW := 13
	canvas.DrawTextPixels(glyphClock, a.fontIcon, color, walk.Rectangle{X: rect.X, Y: rect.Y, Width: iconW, Height: rect.Height}, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	canvas.DrawTextPixels(text, a.fontSmall, color, walk.Rectangle{X: rect.X + iconW + 2, Y: rect.Y, Width: max(0, rect.Width-iconW-2), Height: rect.Height}, walk.TextLeft|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
}

func (a *App) paintGaugeLight(canvas *walk.Canvas, rect walk.Rectangle, pct *float64, textColor walk.Color) {
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
	if rect.Width >= 34 {
		canvas.DrawTextPixels(percentText(pct), a.fontSmall, textColor, inset(rect, 1, 0), walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
	}
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
	now := time.Now()
	if pt == a.lastMousePt && now.Sub(a.lastMouseAt) < 80*time.Millisecond {
		return
	}
	a.lastMousePt = pt
	a.lastMouseAt = now
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
	if contains(a.statusHit, pt) {
		go a.manualRefresh()
		return
	}
	for _, hit := range a.cardHits {
		if !contains(hit.rect, pt) {
			continue
		}
		last := a.lastCardHit[hit.card.SnapshotKey]
		a.lastCardHit[hit.card.SnapshotKey] = now
		if now.Sub(last) <= 420*time.Millisecond {
			a.showBandPicker(hit.card)
		}
		return
	}
}

func (a *App) manualRefresh() {
	a.setStatus(statusRefreshing)
	a.refreshOnce()
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
	dockWork := dockWorkArea(a.cfg.DockMode, a.cfg.DockEdge, full, work)
	rect := dockRect(full, dockWork, a.cfg.DockEdge, isSide(a.cfg.DockEdge), false)
	if a.cfg.DockMode == "reserved" {
		if a.cfg.AutoHide {
			a.cfg.AutoHide = false
			_ = settings.Save(a.paths.Settings, a.cfg)
		}
		a.baseWork = &work
		if native.RegisterAppBar(uintptr(a.mw.Handle())) {
			a.appbar = true
			_, _ = native.SetAppBar(uintptr(a.mw.Handle()), a.cfg.DockEdge, rect)
			if reported, err := native.GetWorkArea(); err == nil {
				if autoScale := appBarAutoScale(a.cfg.DockEdge, reported, rect); autoScale >= 0.5 && autoScale <= 3 && (autoScale < 0.95 || autoScale > 1.05) {
					_, _ = native.SetAppBar(uintptr(a.mw.Handle()), a.cfg.DockEdge, scaleRect(rect, autoScale))
				}
			}
		}
		work := reservedWorkArea(work, a.cfg.DockEdge, rect)
		if err := native.SetWorkArea(work); err != nil {
			a.log.Printf("Reserved workarea apply failed: %v", err)
		} else {
			a.log.Printf("Reserved workarea applied edge=%s rect=(%d,%d,%d,%d) base=(%d,%d,%d,%d)", a.cfg.DockEdge, work.Left, work.Top, work.Right, work.Bottom, a.baseWork.Left, a.baseWork.Top, a.baseWork.Right, a.baseWork.Bottom)
		}
	} else if a.cfg.AutoHide && isSide(a.cfg.DockEdge) {
		hidden := sideOverlayHiddenRect(full, dockWork, a.cfg.DockEdge)
		_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(hidden.Left), Y: int(hidden.Top), Width: int(hidden.Right - hidden.Left), Height: int(hidden.Bottom - hidden.Top)})
		native.SetDockBoundsHidden(uintptr(a.mw.Handle()), hidden)
		a.applyWindowOpacity()
		a.revealed = false
		a.invalidate()
		return
	}
	hiddenHorizontal := false
	if a.cfg.AutoHide {
		rect = dockRect(full, dockWork, a.cfg.DockEdge, isSide(a.cfg.DockEdge), true)
		hiddenHorizontal = true
	}
	_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)})
	native.SetDockBoundsVisible(uintptr(a.mw.Handle()), rect)
	a.applyWindowOpacity()
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
		rect := dockRect(full, dockWorkArea(a.cfg.DockMode, a.cfg.DockEdge, full, work), a.cfg.DockEdge, isSide(a.cfg.DockEdge), true)
		a.mw.Hide()
		native.SetDockBoundsHidden(uintptr(a.mw.Handle()), rect)
	}
	a.refreshTrayVisibilityActions()
}

func (a *App) revealSideDock(rect native.Rect) {
	a.mw.Synchronize(func() {
		_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)})
		a.mw.Show()
		native.SetDockBoundsVisible(uintptr(a.mw.Handle()), rect)
		a.applyWindowOpacity()
	})
	a.revealed = true
	a.invalidate()
}

func (a *App) hideSideDock(rect native.Rect) {
	a.mw.Synchronize(func() {
		_ = a.mw.SetBoundsPixels(walk.Rectangle{X: int(rect.Left), Y: int(rect.Top), Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)})
		native.SetDockBoundsHidden(uintptr(a.mw.Handle()), rect)
		a.applyWindowOpacity()
	})
	a.revealed = false
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
			if !a.isVisible() || a.cfg.DockMode != "overlay" || !a.cfg.AutoHide || a.mw == nil || a.mw.IsDisposed() {
				continue
			}
			pos := native.CursorPosition()
			full := native.PrimaryBounds()
			work, err := native.GetWorkArea()
			if err != nil {
				work = full
			}
			dockWork := dockWorkArea(a.cfg.DockMode, a.cfg.DockEdge, full, work)
			near := false
			switch a.cfg.DockEdge {
			case "top":
				near = pos.Y >= dockWork.Top && pos.Y <= dockWork.Top+4
			case "bottom":
				near = pos.Y >= dockWork.Bottom-4 && pos.Y <= full.Bottom
			case "left":
				near = pos.X >= full.Left && pos.X <= full.Left+dockRevealStrip+4
			case "right":
				near = pos.X >= full.Right-dockRevealStrip-4 && pos.X <= full.Right
			}
			side := isSide(a.cfg.DockEdge)
			if side {
				rect := dockRect(full, dockWork, a.cfg.DockEdge, true, false)
				hidden := sideOverlayHiddenRect(full, dockWork, a.cfg.DockEdge)
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
						a.hideSideDock(hidden)
					}
					continue
				}
				if near {
					a.revealSideDock(rect)
				}
				continue
			}
			rect := dockRect(full, dockWork, a.cfg.DockEdge, false, false)
			if a.revealed {
				extended := horizontalRevealRect(rect, a.cfg.DockEdge, full)
				if contains(extended, walk.Point{X: int(pos.X), Y: int(pos.Y)}) {
					continue
				}
				a.mw.Synchronize(func() {
					hidden := dockRect(full, dockWork, a.cfg.DockEdge, false, true)
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
	centerDialog(dlg, 460, 420)
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
	bl := leftButtonLayout(buttons)
	save, _ := walk.NewPushButton(buttons)
	_ = save.SetText(settingsOK)
	cancel, _ := walk.NewPushButton(buttons)
	_ = cancel.SetText(settingsCancel)
	addButtonSpacer(buttons, bl)
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
	dlg, err := walk.NewDialogWithFixedSize(nil)
	if err != nil {
		return
	}
	defer dlg.Dispose()
	original := a.cfg
	original.Normalize()
	saved := false
	_ = dlg.SetTitle(settingsTitle)
	_ = dlg.SetClientSize(walk.Size{Width: 660, Height: 560})
	placeSettingsDialog(dlg, 700, 640)
	root := walk.NewVBoxLayout()
	_ = root.SetMargins(walk.Margins{HNear: 14, VNear: 14, HFar: 14, VFar: 14})
	_ = root.SetSpacing(7)
	_ = dlg.SetLayout(root)

	addLabel(dlg, settingsTheme)
	themePicker, _ := newThemePicker(dlg, a.cfg.Theme, a.fontSmall, a.fontBold)
	addLabel(dlg, settingsPosition)
	edgePicker, _ := newEdgePicker(dlg, a.cfg.DockEdge, a.fontSmall, a.fontBold)
	mode := combo(dlg, settingsDisplayMode, []string{"reserved", "overlay"}, a.cfg.DockMode)
	startup := checkbox(dlg, settingsStartup, native.StartupEnabled())
	slide := checkbox(dlg, settingsAutoSlide, a.cfg.AutoHide)
	opacity, opacityValue := slider(dlg, settingsOpacity, a.cfg.OverlayOpacity, 35, 100)
	opacity.ValueChanged().Attach(func() {
		value := opacity.Value()
		_ = opacityValue.SetText(fmt.Sprintf("%d%%", value))
		a.cfg.OverlayOpacity = value
		a.applyWindowOpacity()
	})
	refresh := number(dlg, settingsRefresh, float64(a.cfg.RefreshSeconds), 5, 600)
	maxBands := number(dlg, settingsGaugeBands, float64(a.cfg.GaugeMaxBands), 1, 4)
	warn := number(dlg, settingsGaugeWarn, float64(a.cfg.GaugeWarnPercent), 1, 100)
	crit := number(dlg, settingsGaugeCrit, float64(a.cfg.GaugeCritPercent), 1, 100)
	a.addDiagnosticsControls(dlg)

	buttons, _ := walk.NewComposite(dlg)
	bl := leftButtonLayout(buttons)
	save, _ := walk.NewPushButton(buttons)
	_ = save.SetText(settingsSave)
	cancel, _ := walk.NewPushButton(buttons)
	_ = cancel.SetText(settingsCancel)
	addButtonSpacer(buttons, bl)
	save.Clicked().Attach(func() {
		next := original
		next.Theme = themePicker.Value()
		next.DockEdge = edgePicker.Value()
		next.DockMode = strings.ToLower(strings.TrimSpace(mode.Text()))
		next.StartWithWindows = startup.Checked()
		next.AutoHide = slide.Checked()
		next.OverlayOpacity = opacity.Value()
		next.RefreshSeconds = int(refresh.Value())
		next.GaugeMaxBands = int(maxBands.Value())
		next.GaugeWarnPercent = int(warn.Value())
		next.GaugeCritPercent = int(crit.Value())
		next.Normalize()

		cfgChanged := !reflect.DeepEqual(next, original)
		if !cfgChanged {
			saved = true
			dlg.Accept()
			return
		}

		themeChanged := next.Theme != original.Theme
		dockChanged := next.DockMode != original.DockMode || next.DockEdge != original.DockEdge || next.AutoHide != original.AutoHide
		opacityChanged := next.OverlayOpacity != original.OverlayOpacity
		refreshChanged := next.RefreshSeconds != original.RefreshSeconds ||
			next.GaugeMaxBands != original.GaugeMaxBands ||
			next.GaugeWarnPercent != original.GaugeWarnPercent ||
			next.GaugeCritPercent != original.GaugeCritPercent

		a.cfg = next
		if err := settings.Save(a.paths.Settings, a.cfg); err != nil {
			a.cfg = original
			walk.MsgBox(dlg, appName, err.Error(), walk.MsgBoxIconError)
			return
		}
		if err := native.SetStartupEnabled(a.paths.Root, a.cfg.StartWithWindows); err != nil {
			walk.MsgBox(dlg, appName, err.Error(), walk.MsgBoxIconWarning)
		}
		saved = true
		dlg.Accept()
		if themeChanged {
			a.applyTheme()
		}
		if dockChanged {
			a.applyDock()
		} else if opacityChanged {
			a.applyWindowOpacity()
		}
		if themeChanged || dockChanged || opacityChanged || refreshChanged {
			a.refreshOnce()
		}
	})
	cancel.Clicked().Attach(func() { dlg.Cancel() })
	_ = dlg.SetDefaultButton(save)
	_ = dlg.SetCancelButton(cancel)
	native.FocusTopmost(uintptr(dlg.Handle()))
	dlg.Run()
	if !saved && !reflect.DeepEqual(a.cfg, original) {
		themeChanged := a.cfg.Theme != original.Theme
		dockChanged := a.cfg.DockMode != original.DockMode || a.cfg.DockEdge != original.DockEdge || a.cfg.AutoHide != original.AutoHide
		opacityChanged := a.cfg.OverlayOpacity != original.OverlayOpacity
		a.cfg = original
		if themeChanged {
			a.applyTheme()
		}
		if dockChanged {
			a.applyDock()
		} else if opacityChanged {
			a.applyWindowOpacity()
		}
	}
}

func (a *App) addDiagnosticsControls(parent walk.Container) {
	addLabel(parent, settingsDiagnostics)
	pathText, _ := walk.NewTextEdit(parent)
	pathText.SetCompactHeight(true)
	_ = pathText.SetReadOnly(true)
	_ = pathText.SetText(fmt.Sprintf("OpenUsage: %s\r\nLogs: %s", openUsageSettingsPath(), a.paths.Logs))

	row1, _ := walk.NewComposite(parent)
	row1Layout := leftButtonLayout(row1)
	openOU, _ := walk.NewPushButton(row1)
	_ = openOU.SetText(settingsOpenOU)
	openOUFolder, _ := walk.NewPushButton(row1)
	_ = openOUFolder.SetText(settingsOpenOUFolder)
	openLogs, _ := walk.NewPushButton(row1)
	_ = openLogs.SetText(settingsOpenLogs)
	addButtonSpacer(row1, row1Layout)

	row2, _ := walk.NewComposite(parent)
	row2Layout := leftButtonLayout(row2)
	openAppLog, _ := walk.NewPushButton(row2)
	_ = openAppLog.SetText(settingsOpenAppLog)
	copyDiag, _ := walk.NewPushButton(row2)
	_ = copyDiag.SetText(settingsCopyDiag)
	addButtonSpacer(row2, row2Layout)

	openOU.Clicked().Attach(func() {
		path := openUsageSettingsPath()
		if err := ensureFile(path, "{}\r\n"); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconError)
			return
		}
		if err := openFile(path); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconWarning)
		}
	})
	openOUFolder.Clicked().Attach(func() {
		path := filepath.Dir(openUsageSettingsPath())
		if err := os.MkdirAll(path, 0o755); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconError)
			return
		}
		if err := openFolder(path); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconWarning)
		}
	})
	openLogs.Clicked().Attach(func() {
		if err := os.MkdirAll(a.paths.Logs, 0o755); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconError)
			return
		}
		if err := openFolder(a.paths.Logs); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconWarning)
		}
	})
	openAppLog.Clicked().Attach(func() {
		path := filepath.Join(a.paths.Logs, "limitdock.log")
		if err := ensureFile(path, ""); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconError)
			return
		}
		if err := openFile(path); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconWarning)
		}
	})
	copyDiag.Clicked().Attach(func() {
		if err := walk.Clipboard().SetText(a.diagnosticsText()); err != nil {
			walk.MsgBox(a.mw, appName, err.Error(), walk.MsgBoxIconWarning)
		}
	})
}

func (a *App) diagnosticsText() string {
	return strings.Join([]string{
		"LimitDock diagnostics",
		"Root: " + a.paths.Root,
		"settings.json: " + a.paths.Settings,
		"Engine: " + a.paths.Engine,
		"Logs: " + a.paths.Logs,
		"OpenUsage settings: " + openUsageSettingsPath(),
		"OpenUsage exe: " + a.paths.OpenUsageExe,
		"OpenUsage socket: " + a.paths.SocketPath,
		"Daemon stdout: " + a.paths.DaemonOutLog,
		"Daemon stderr: " + a.paths.DaemonErrLog,
	}, "\r\n")
}

func (a *App) cleanup() {
	a.cancel()
	if !a.isShutdownDone() && a.manager != nil {
		a.manager.Stop()
	}
	if base, ok := a.unregisterAppBar(); ok {
		if err := native.ScheduleWorkAreaRestore(base); err != nil {
			a.log.Printf("Reserved delayed workarea restore failed to start: %v", err)
		}
	}
	if a.notify != nil {
		_ = a.notify.SetVisible(false)
		_ = a.notify.Dispose()
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

func (a *App) beginShutdown() {
	a.shutdownOnce.Do(func() {
		a.cancel()
		a.log.Printf("Exit requested; keeping tray icon visible until shutdown completes")
		if a.notify != nil {
			_ = a.notify.SetToolTip(appName + " exiting")
		}
		go a.finishShutdown()
	})
}

func (a *App) finishShutdown() {
	if a.manager != nil {
		a.manager.Stop()
	}
	a.shutdownMu.Lock()
	a.shutdownDone = true
	a.shutdownMu.Unlock()
	a.log.Printf("Shutdown cleanup completed; closing UI")
	if a.mw != nil && !a.mw.IsDisposed() {
		native.PostClose(uintptr(a.mw.Handle()))
		native.WakeWindow(uintptr(a.mw.Handle()))
	}
}

func (a *App) isShutdownDone() bool {
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	return a.shutdownDone
}

func (a *App) setStatus(text string) {
	a.mu.Lock()
	a.status = text
	a.mu.Unlock()
	a.invalidate()
}

func (a *App) applyTheme() {
	applyTheme(a.cfg.Theme)
	if a.mw == nil || a.mw.IsDisposed() {
		return
	}
	if brush, err := walk.NewSolidColorBrush(themeBar); err == nil {
		a.mw.SetBackground(brush)
	}
	a.invalidate()
}

func (a *App) applyWindowOpacity() {
	if a.mw == nil || a.mw.IsDisposed() {
		return
	}
	opacity := 100
	if a.cfg.DockMode == "overlay" {
		opacity = a.cfg.OverlayOpacity
	}
	native.SetWindowOpacity(uintptr(a.mw.Handle()), opacity)
	if a.surface != nil && !a.surface.IsDisposed() {
		native.SetWindowOpacity(uintptr(a.surface.Handle()), opacity)
	}
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
	path := filepath.Join(a.paths.IconDir, base)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	img, err := walk.NewBitmapFromFileForDPI(path, 96)
	if err == nil {
		a.images[base] = img
		return img
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

type themePicker struct {
	widget *walk.CustomWidget
	value  string
	small  *walk.Font
	bold   *walk.Font
}

func newThemePicker(parent walk.Container, current string, small, bold *walk.Font) (*themePicker, error) {
	p := &themePicker{value: validTheme(current), small: small, bold: bold}
	w, err := walk.NewCustomWidgetPixels(parent, 0, p.paint)
	if err != nil {
		return nil, err
	}
	p.widget = w
	_ = w.SetMinMaxSize(walk.Size{Width: 112, Height: 42}, walk.Size{Height: 42})
	w.SetClearsBackground(true)
	w.SetInvalidatesOnResize(true)
	w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		p.selectAt(x, w.ClientBoundsPixels().Width)
	})
	return p, nil
}

func (p *themePicker) Value() string {
	if p == nil {
		return "light"
	}
	return validTheme(p.value)
}

func (p *themePicker) selectAt(x, width int) {
	if width <= 0 {
		width = 112
	}
	rects := p.rects(walk.Rectangle{Width: width, Height: 42})
	if contains(rects["night"], walk.Point{X: x, Y: rects["night"].Y + 1}) {
		p.value = "night"
	} else if contains(rects["light"], walk.Point{X: x, Y: rects["light"].Y + 1}) {
		p.value = "light"
	}
	if p.widget != nil {
		_ = p.widget.Invalidate()
	}
}

func (p *themePicker) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	bounds := p.widget.ClientBoundsPixels()
	bounds.X = 0
	bounds.Y = 0
	rects := p.rects(bounds)
	for _, theme := range []string{"light", "night"} {
		p.paintThemeOption(canvas, rects[theme], theme, theme == p.value)
	}
	return nil
}

func (p *themePicker) rects(bounds walk.Rectangle) map[string]walk.Rectangle {
	gap := 8
	total := min(116, bounds.Width)
	width := (total - gap) / 2
	y := bounds.Y + 2
	height := 24
	return map[string]walk.Rectangle{
		"light": {X: bounds.X, Y: y, Width: width, Height: height},
		"night": {X: bounds.X + width + gap, Y: y, Width: width, Height: height},
	}
}

func (p *themePicker) paintThemeOption(canvas *walk.Canvas, rect walk.Rectangle, theme string, selected bool) {
	fillColor := themeStatusBack
	lineColor := themeGaugeBorder
	textColor := themeFore
	if selected {
		fillColor = themeInfoBack
		lineColor = themeAccent
		textColor = themeAccent
	}
	fill := brush(fillColor)
	defer fill.Dispose()
	canvas.FillRoundedRectanglePixels(fill, rect, walk.Size{Width: 10, Height: 10})
	pen, _ := walk.NewCosmeticPen(walk.PenSolid, lineColor)
	if pen != nil {
		defer pen.Dispose()
		canvas.DrawRoundedRectanglePixels(pen, rect, walk.Size{Width: 10, Height: 10})
	}
	if selected {
		paintSelectionOutline(canvas, rect, 10)
	}
	drawThemeSymbol(canvas, inset(rect, 8, 5), theme, textColor, fillColor)
}

func drawThemeSymbol(canvas *walk.Canvas, rect walk.Rectangle, theme string, fore, back walk.Color) {
	size := min(rect.Width, rect.Height)
	if size < 10 {
		size = 10
	}
	cx := rect.X + rect.Width/2
	cy := rect.Y + rect.Height/2
	icon := walk.Rectangle{X: cx - size/2, Y: cy - size/2, Width: size, Height: size}
	brushFore := brush(fore)
	defer brushFore.Dispose()
	if theme == "night" {
		canvas.FillEllipsePixels(brushFore, icon)
		brushBack := brush(back)
		defer brushBack.Dispose()
		canvas.FillEllipsePixels(brushBack, walk.Rectangle{X: icon.X + size/3, Y: icon.Y - 1, Width: size, Height: size})
		return
	}
	core := walk.Rectangle{X: cx - size/4, Y: cy - size/4, Width: size / 2, Height: size / 2}
	canvas.FillEllipsePixels(brushFore, core)
	pen, _ := walk.NewCosmeticPen(walk.PenSolid, fore)
	if pen == nil {
		return
	}
	defer pen.Dispose()
	for _, line := range [][4]int{
		{cx, icon.Y, cx, icon.Y + size/5},
		{cx, icon.Bottom(), cx, icon.Bottom() - size/5},
		{icon.X, cy, icon.X + size/5, cy},
		{icon.Right(), cy, icon.Right() - size/5, cy},
	} {
		canvas.DrawLinePixels(pen, walk.Point{X: line[0], Y: line[1]}, walk.Point{X: line[2], Y: line[3]})
	}
}

func validTheme(theme string) string {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "night":
		return "night"
	default:
		return "light"
	}
}

type edgePicker struct {
	widget *walk.CustomWidget
	value  string
	small  *walk.Font
	bold   *walk.Font
}

func newEdgePicker(parent walk.Container, current string, small, bold *walk.Font) (*edgePicker, error) {
	p := &edgePicker{value: current, small: small, bold: bold}
	if !validEdge(p.value) {
		p.value = "bottom"
	}
	w, err := walk.NewCustomWidgetPixels(parent, 0, p.paint)
	if err != nil {
		return nil, err
	}
	p.widget = w
	_ = w.SetMinMaxSize(walk.Size{Width: 154, Height: 48}, walk.Size{Height: 48})
	w.SetClearsBackground(true)
	w.SetInvalidatesOnResize(true)
	w.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button != walk.LeftButton {
			return
		}
		p.selectAt(x, w.ClientBoundsPixels().Width)
	})
	return p, nil
}

func (p *edgePicker) Value() string {
	if p == nil || !validEdge(p.value) {
		return "bottom"
	}
	return p.value
}

func (p *edgePicker) selectAt(x, width int) {
	if width <= 0 {
		width = 154
	}
	rects := p.rects(walk.Rectangle{Width: width, Height: 48})
	pt := walk.Point{X: x, Y: 3}
	for _, edge := range []string{"bottom", "left", "top", "right"} {
		if contains(rects[edge], pt) {
			p.value = edge
			break
		}
	}
	if p.widget != nil {
		_ = p.widget.Invalidate()
	}
}

func (p *edgePicker) paint(canvas *walk.Canvas, update walk.Rectangle) error {
	bounds := p.widget.ClientBoundsPixels()
	bounds.X = 0
	bounds.Y = 0
	rects := p.rects(bounds)
	for _, edge := range []string{"bottom", "left", "top", "right"} {
		p.paintEdgeOption(canvas, rects[edge], edge, edge == p.value)
	}
	return nil
}

func (p *edgePicker) rects(bounds walk.Rectangle) map[string]walk.Rectangle {
	gap := 8
	total := min(292, bounds.Width)
	width := max(44, (total-gap*3)/4)
	y := bounds.Y + 2
	height := 32
	out := map[string]walk.Rectangle{}
	for i, edge := range []string{"bottom", "left", "top", "right"} {
		out[edge] = walk.Rectangle{X: bounds.X + i*(width+gap), Y: y, Width: width, Height: height}
	}
	return out
}

func (p *edgePicker) paintEdgeOption(canvas *walk.Canvas, rect walk.Rectangle, edge string, selected bool) {
	fillColor := themeStatusBack
	lineColor := themeGaugeBorder
	textColor := themeFore
	if selected {
		fillColor = themeInfoBack
		lineColor = themeAccent
		textColor = themeAccent
	}
	fill := brush(fillColor)
	defer fill.Dispose()
	canvas.FillRoundedRectanglePixels(fill, rect, walk.Size{Width: 12, Height: 12})
	pen, _ := walk.NewCosmeticPen(walk.PenSolid, lineColor)
	if pen != nil {
		defer pen.Dispose()
		canvas.DrawRoundedRectanglePixels(pen, rect, walk.Size{Width: 12, Height: 12})
	}
	if selected {
		paintSelectionOutline(canvas, rect, 12)
	}
	screenH := min(22, max(14, rect.Height-10))
	screen := walk.Rectangle{X: rect.X + 11, Y: rect.Y + max(4, (rect.Height-screenH)/2), Width: max(22, rect.Width-22), Height: screenH}
	screenFill := brush(themeBar)
	defer screenFill.Dispose()
	canvas.FillRectanglePixels(screenFill, screen)
	screenPen, _ := walk.NewCosmeticPen(walk.PenSolid, themeMuted)
	if screenPen != nil {
		defer screenPen.Dispose()
		canvas.DrawRectanglePixels(screenPen, screen)
	}
	dock := dockMarkerRect(screen, edge)
	accent := brush(themeAccent)
	defer accent.Dispose()
	canvas.FillRectanglePixels(accent, dock)
	labelFont := p.small
	if selected && p.bold != nil {
		labelFont = p.bold
	}
	if rect.Height >= 44 {
		canvas.DrawTextPixels(edge, labelFont, textColor, walk.Rectangle{X: rect.X + 3, Y: rect.Bottom() - 20, Width: rect.Width - 6, Height: 14}, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine|walk.TextEndEllipsis)
	}
}

func paintSelectionOutline(canvas *walk.Canvas, rect walk.Rectangle, radius int) {
	outer := inset(rect, 2, 2)
	pen, _ := walk.NewCosmeticPen(walk.PenSolid, themeAccent)
	if pen != nil {
		defer pen.Dispose()
		canvas.DrawRoundedRectanglePixels(pen, outer, walk.Size{Width: radius, Height: radius})
	}
}

func dockMarkerRect(screen walk.Rectangle, edge string) walk.Rectangle {
	switch edge {
	case "left":
		return walk.Rectangle{X: screen.X + 1, Y: screen.Y + 1, Width: 5, Height: screen.Height - 2}
	case "right":
		return walk.Rectangle{X: screen.Right() - 6, Y: screen.Y + 1, Width: 5, Height: screen.Height - 2}
	case "top":
		return walk.Rectangle{X: screen.X + 1, Y: screen.Y + 1, Width: screen.Width - 2, Height: 5}
	default:
		return walk.Rectangle{X: screen.X + 1, Y: screen.Bottom() - 6, Width: screen.Width - 2, Height: 5}
	}
}

func validEdge(edge string) bool {
	switch edge {
	case "left", "right", "bottom", "top":
		return true
	default:
		return false
	}
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

func slider(parent walk.Container, label string, value, min, maxv int) (*walk.Slider, *walk.Label) {
	row, _ := walk.NewComposite(parent)
	layout := walk.NewHBoxLayout()
	_ = layout.SetMargins(walk.Margins{})
	_ = layout.SetSpacing(8)
	_ = row.SetLayout(layout)
	l, _ := walk.NewLabel(row)
	_ = l.SetText(label)
	l.SetTextColor(themeMuted)
	sl, _ := walk.NewSlider(row)
	sl.SetRange(min, maxv)
	sl.SetValue(value)
	valueLabel, _ := walk.NewLabel(row)
	_ = valueLabel.SetText(fmt.Sprintf("%d%%", value))
	_ = valueLabel.SetMinMaxSize(walk.Size{Width: 48, Height: 0}, walk.Size{})
	_ = layout.SetStretchFactor(sl, 1)
	return sl, valueLabel
}

func leftButtonLayout(parent walk.Container) *walk.BoxLayout {
	layout := walk.NewHBoxLayout()
	_ = layout.SetMargins(walk.Margins{})
	_ = layout.SetSpacing(8)
	_ = parent.SetLayout(layout)
	return layout
}

func addButtonSpacer(parent walk.Container, layout *walk.BoxLayout) {
	spacer, err := walk.NewHSpacer(parent)
	if err == nil && layout != nil {
		_ = layout.SetStretchFactor(spacer, 1)
	}
}

func centerDialog(dlg *walk.Dialog, width, height int) {
	work, err := native.GetWorkArea()
	if err != nil {
		full := native.PrimaryBounds()
		work = full
	}
	x := int(work.Left) + max(0, int(work.Right-work.Left)-width)/2
	y := int(work.Top) + max(0, int(work.Bottom-work.Top)-height)/2
	_ = dlg.SetBoundsPixels(walk.Rectangle{X: x, Y: y, Width: width, Height: height})
}

func placeSettingsDialog(dlg *walk.Dialog, width, height int) {
	full := native.PrimaryBounds()
	x := int(full.Left) + max(0, int(full.Right-full.Left)-width)/2
	y := int(full.Top) + 60
	_ = dlg.SetBoundsPixels(walk.Rectangle{X: x, Y: y, Width: width, Height: height})
}

func openUsageSettingsPath() string {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		home, _ := os.UserHomeDir()
		appData = filepath.Join(home, "AppData", "Roaming")
	}
	return filepath.Join(appData, "openusage", "settings.json")
}

func ensureFile(path string, initial string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(initial), 0o644)
}

func openFile(path string) error {
	return exec.Command("notepad.exe", path).Start()
}

func openFolder(path string) error {
	return exec.Command("explorer.exe", path).Start()
}

func addLabel(parent walk.Container, text string) {
	l, _ := walk.NewLabel(parent)
	_ = l.SetText(text)
	l.SetTextColor(themeMuted)
}

func dockRect(full native.Rect, work native.Rect, edge string, side bool, hidden bool) native.Rect {
	width := dockSideWidth
	height := dockRibbonHeight
	if side {
		height = work.Bottom - work.Top
	} else {
		width = work.Right - work.Left
	}
	switch edge {
	case "top":
		y := work.Top + dockEdgeGap
		if hidden {
			y = full.Top - height - dockEdgeGap
		}
		return native.Rect{Left: work.Left, Top: y, Right: work.Left + width, Bottom: y + height}
	case "left":
		x := work.Left + dockEdgeGap
		if hidden {
			x = full.Left - width - dockEdgeGap
		}
		return native.Rect{Left: x, Top: work.Top, Right: x + width, Bottom: work.Top + height}
	case "right":
		x := work.Right - width - dockEdgeGap
		if hidden {
			x = full.Right + dockEdgeGap
		}
		return native.Rect{Left: x, Top: work.Top, Right: x + width, Bottom: work.Top + height}
	default:
		y := work.Bottom - height - dockEdgeGap
		if hidden {
			y = full.Bottom + dockEdgeGap
		}
		return native.Rect{Left: work.Left, Top: y, Right: work.Left + width, Bottom: y + height}
	}
}

func sideOverlayHiddenRect(full native.Rect, work native.Rect, edge string) native.Rect {
	return dockRect(full, work, edge, true, true)
}

func reservedWorkArea(work native.Rect, edge string, rect native.Rect) native.Rect {
	switch edge {
	case "top":
		work.Top = rect.Bottom
	case "left":
		work.Left = rect.Right
	case "right":
		work.Right = rect.Left
	default:
		work.Bottom = rect.Top
	}
	return work
}

func dockWorkArea(mode string, edge string, full native.Rect, work native.Rect) native.Rect {
	if mode == "overlay" && isSide(edge) {
		return full
	}
	return work
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
		return statusUpdatedEmpty
	}
	return status
}

func ribbonStatusParts(status string) (string, string) {
	text := ribbonStatusText(status)
	if after, ok := strings.CutPrefix(text, statusUpdatedPrefix); ok {
		return strings.TrimSpace(statusUpdatedPrefix), strings.TrimSpace(after)
	}
	switch text {
	case statusStartingOU:
		return statusStarting, textOpenUsage
	case statusOUReady:
		return statusReady, textOpenUsage
	case statusOUUnavailable:
		return statusUnavailable, textOpenUsage
	case statusWaitingOU:
		return statusWaiting, textOpenUsage
	case statusRefreshing:
		return statusRefreshing, ""
	default:
		return text, ""
	}
}

func ribbonCardWidth(available, count, gap int) int {
	if count <= 0 {
		return 500
	}
	const preferred = 500
	const minimum = 340
	if count >= 5 {
		width := (available - gap*4) / 5
		if width >= minimum {
			return min(preferred, width)
		}
	}
	width := (available - gap*(count-1)) / count
	if width < preferred {
		return max(minimum, width)
	}
	return preferred
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

func resetDisplayText(reset string) string {
	reset = strings.TrimSpace(reset)
	reset = strings.TrimPrefix(reset, "~")
	return strings.TrimSpace(reset)
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

func applyTheme(name string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "night":
		applyNightTheme()
	default:
		applyLightTheme()
	}
}

func applyLightTheme() {
	themeBar = walk.RGB(243, 243, 243)
	themeFore = walk.RGB(32, 32, 32)
	themeMuted = walk.RGB(96, 96, 96)
	themeAccent = walk.RGB(0, 99, 177)
	themeOkBack = walk.RGB(255, 255, 255)
	themeInfoBack = walk.RGB(232, 232, 232)
	themeStatusBack = walk.RGB(232, 232, 232)
	themeWarnBack = walk.RGB(255, 244, 214)
	themeWarnFore = walk.RGB(94, 66, 0)
	themeCriticalBack = walk.RGB(255, 226, 226)
	themeCriticalFore = walk.RGB(117, 21, 31)
	themeSideBack = walk.RGB(238, 238, 238)
	themeSideTop = walk.RGB(232, 232, 232)
	themeSideCard = walk.RGB(255, 255, 255)
	themeSideLine = walk.RGB(224, 224, 224)
	themeSideText = walk.RGB(0, 0, 0)
	themeSideMuted = walk.RGB(80, 80, 80)
	themeSideBlue = walk.RGB(0, 99, 177)
	themeSideIcon = walk.RGB(70, 70, 70)
	themeGaugeTrackLight = walk.RGB(210, 210, 210)
	themeGaugeBorder = walk.RGB(128, 128, 128)
	themeGaugeOk = walk.RGB(16, 124, 16)
	themeGaugeWarn = walk.RGB(196, 128, 0)
	themeGaugeCrit = walk.RGB(180, 32, 32)
	themeCodexIcon = walk.RGB(0, 166, 118)
	themeGeminiIcon = walk.RGB(92, 113, 255)
	themeIconFore = walk.RGB(255, 255, 255)
}

func applyNightTheme() {
	themeBar = walk.RGB(23, 27, 32)
	themeFore = walk.RGB(236, 239, 244)
	themeMuted = walk.RGB(166, 174, 186)
	themeAccent = walk.RGB(94, 168, 255)
	themeOkBack = walk.RGB(33, 38, 46)
	themeInfoBack = walk.RGB(40, 46, 55)
	themeStatusBack = walk.RGB(40, 46, 55)
	themeWarnBack = walk.RGB(73, 56, 28)
	themeWarnFore = walk.RGB(255, 219, 140)
	themeCriticalBack = walk.RGB(74, 37, 43)
	themeCriticalFore = walk.RGB(255, 176, 184)
	themeSideBack = walk.RGB(24, 28, 34)
	themeSideTop = walk.RGB(31, 36, 43)
	themeSideCard = walk.RGB(35, 40, 48)
	themeSideLine = walk.RGB(58, 64, 74)
	themeSideText = walk.RGB(238, 241, 245)
	themeSideMuted = walk.RGB(166, 174, 186)
	themeSideBlue = walk.RGB(114, 183, 255)
	themeSideIcon = walk.RGB(210, 216, 225)
	themeGaugeTrackLight = walk.RGB(70, 76, 86)
	themeGaugeBorder = walk.RGB(110, 118, 130)
	themeGaugeOk = walk.RGB(50, 180, 92)
	themeGaugeWarn = walk.RGB(224, 168, 58)
	themeGaugeCrit = walk.RGB(226, 88, 96)
	themeCodexIcon = walk.RGB(0, 166, 118)
	themeGeminiIcon = walk.RGB(104, 128, 255)
	themeIconFore = walk.RGB(255, 255, 255)
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
