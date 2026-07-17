package ui

import (
	"fmt"
	"strings"

	"github.com/lxn/walk"

	"limitdock/internal/claudeauth"
	"limitdock/internal/native"
	"limitdock/internal/provider"
	"limitdock/internal/quota"
	"limitdock/internal/readmodel"
)

const (
	trayConnectClaude       = "Connect Claude..."
	settingsProviders       = "Providers"
	providerStatusNone      = "Not detected"
	providerStatusOK        = "Connected"
	providerStatusNeedsAuth = "Sign-in required"
	providerStatusStale     = "Showing last known data"
	providerStatusError     = "Error"
	connectTitle            = "Connect Claude"
	connectOpenBrowser      = "1. Open Claude sign-in"
	connectInstructions     = "2. Approve LimitDock in the browser.\r\n3. Copy the code the page shows (looks like abc12...#xyz89...) and paste it below."
	connectPasteHint        = "Paste the code here"
	connectButton           = "Connect"
	connectBusy             = "Connecting..."
	disconnectButton        = "Disconnect"
	connectSuccessPrefix    = "Connected as "
)

// knownProviders is the fixed display order for the Settings Providers
// section; it mirrors the reader registration order.
var knownProviders = []struct {
	ProviderID string
	Name       string
}{
	{"claude_code", "Claude"},
	{"codex", "Codex"},
	{"gemini_cli", "Gemini"},
	{"cursor", "Cursor"},
	{"antigravity", "Antigravity"},
}

type providerStatusRow struct {
	ProviderID string
	Name       string
	StatusText string
}

// providerStatusRows maps current cards onto the fixed provider list; a
// provider with no card stays "Not detected" (absence stays silent).
func providerStatusRows(cards []quota.Card) []providerStatusRow {
	byProvider := map[string]quota.Card{}
	for _, card := range cards {
		if _, exists := byProvider[card.ProviderID]; !exists {
			byProvider[card.ProviderID] = card
		}
	}
	rows := make([]providerStatusRow, 0, len(knownProviders))
	for _, p := range knownProviders {
		row := providerStatusRow{ProviderID: p.ProviderID, Name: p.Name, StatusText: providerStatusNone}
		if card, ok := byProvider[p.ProviderID]; ok {
			row.StatusText = providerStatusText(card)
		}
		rows = append(rows, row)
	}
	return rows
}

func providerStatusText(card quota.Card) string {
	switch card.Status {
	case readmodel.StatusNeedsAuth:
		return providerStatusNeedsAuth
	case readmodel.StatusStale:
		return providerStatusStale
	case readmodel.StatusError:
		return providerStatusError
	default:
		if card.Main != "" && card.Main != "Loading" {
			return providerStatusOK + " — " + card.Main + " left"
		}
		return providerStatusOK
	}
}

func cardWantsClaudeConnect(card quota.Card) bool {
	return card.ProviderID == "claude_code" && card.Status == readmodel.StatusNeedsAuth
}

func cardsNeedClaudeConnect(cards []quota.Card) bool {
	for _, card := range cards {
		if cardWantsClaudeConnect(card) {
			return true
		}
	}
	return false
}

func (a *App) currentCards() []quota.Card {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]quota.Card(nil), a.cards...)
}

func (a *App) claudeAuthManager() claudeauth.Manager {
	return claudeauth.Manager{Log: a.log, StoreDir: a.paths.Credentials}
}

// refreshTrayProviderActions shows the tray Connect entry only while Claude
// needs a sign-in. Called from the refresh goroutine, so the walk mutation is
// marshalled onto the UI thread.
func (a *App) refreshTrayProviderActions(cards []quota.Card) {
	if a.trayConnectClaude == nil || a.mw == nil || a.mw.IsDisposed() {
		return
	}
	needsConnect := cardsNeedClaudeConnect(cards)
	a.mw.Synchronize(func() {
		_ = a.trayConnectClaude.SetVisible(needsConnect)
	})
}

// addProvidersControls renders the per-provider status list in the Settings
// dialog. Claude always offers Connect (and Disconnect while a LimitDock
// token exists) so a user without any CLI login can still sign in.
func (a *App) addProvidersControls(parent walk.Container) {
	addLabel(parent, settingsProviders)
	rows := providerStatusRows(a.currentCards())
	connectedEmail, connected := a.claudeAuthManager().StoredAccountEmail()
	for _, row := range rows {
		comp, _ := walk.NewComposite(parent)
		layout := leftButtonLayout(comp)
		label, _ := walk.NewLabel(comp)
		text := row.Name + ": " + row.StatusText
		if row.ProviderID == "claude_code" && connected {
			if connectedEmail != "" {
				text += " (" + connectedEmail + ")"
			} else {
				text += " (LimitDock token)"
			}
		}
		_ = label.SetText(text)
		if row.ProviderID == "claude_code" {
			connect, _ := walk.NewPushButton(comp)
			_ = connect.SetText(connectButton + "...")
			connect.Clicked().Attach(func() { a.showConnectClaudeDialog() })
			if connected {
				disconnect, _ := walk.NewPushButton(comp)
				_ = disconnect.SetText(disconnectButton)
				disconnect.Clicked().Attach(func() { a.disconnectClaude() })
			}
		}
		addButtonSpacer(comp, layout)
	}
}

func (a *App) disconnectClaude() {
	if walk.MsgBox(a.mw, connectTitle, "Remove the Claude token stored by LimitDock?\r\n(Your claude CLI login is not affected.)", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	if err := a.claudeAuthManager().Disconnect(); err != nil {
		walk.MsgBox(a.mw, connectTitle, err.Error(), walk.MsgBoxIconError)
		return
	}
	go a.manualRefresh()
}

// showConnectClaudeDialog runs the PKCE connect flow: open the browser, let
// the user paste the CODE#STATE string, exchange it off the UI thread, and
// store the resulting long-lived token.
func (a *App) showConnectClaudeDialog() {
	mgr := a.claudeAuthManager()
	ch, err := claudeauth.NewPKCEChallenge()
	if err != nil {
		walk.MsgBox(a.mw, connectTitle, err.Error(), walk.MsgBoxIconError)
		return
	}
	authorizeURL := mgr.BuildAuthorizeURL(ch)

	dlg, err := walk.NewDialogWithFixedSize(nil)
	if err != nil {
		return
	}
	defer dlg.Dispose()
	_ = dlg.SetTitle(connectTitle)
	_ = dlg.SetClientSize(walk.Size{Width: 520, Height: 300})
	centerDialog(dlg, 560, 360)
	layout := walk.NewVBoxLayout()
	_ = layout.SetMargins(walk.Margins{HNear: 14, VNear: 14, HFar: 14, VFar: 14})
	_ = layout.SetSpacing(8)
	_ = dlg.SetLayout(layout)

	openRow, _ := walk.NewComposite(dlg)
	openLayout := leftButtonLayout(openRow)
	openBtn, _ := walk.NewPushButton(openRow)
	_ = openBtn.SetText(connectOpenBrowser)
	addButtonSpacer(openRow, openLayout)

	instructions, _ := walk.NewTextEdit(dlg)
	instructions.SetCompactHeight(true)
	_ = instructions.SetReadOnly(true)
	_ = instructions.SetText(connectInstructions)

	pasteEdit, _ := walk.NewTextEdit(dlg)
	pasteEdit.SetCompactHeight(true)
	_ = pasteEdit.SetText("")

	statusLabel, _ := walk.NewLabel(dlg)
	_ = statusLabel.SetText(connectPasteHint)

	openBtn.Clicked().Attach(func() {
		if err := openURL(authorizeURL); err != nil {
			// Fall back to the clipboard so the user can open it manually.
			_ = walk.Clipboard().SetText(authorizeURL)
			_ = statusLabel.SetText("Could not open the browser; the sign-in URL was copied to your clipboard.")
			return
		}
		_ = statusLabel.SetText("Browser opened — approve LimitDock, then paste the code below.")
	})

	buttons, _ := walk.NewComposite(dlg)
	bl := leftButtonLayout(buttons)
	connectBtn, _ := walk.NewPushButton(buttons)
	_ = connectBtn.SetText(connectButton)
	cancelBtn, _ := walk.NewPushButton(buttons)
	_ = cancelBtn.SetText(settingsCancel)
	addButtonSpacer(buttons, bl)

	connectBtn.Clicked().Attach(func() {
		pasted := strings.TrimSpace(pasteEdit.Text())
		if pasted == "" {
			_ = statusLabel.SetText(connectPasteHint)
			return
		}
		connectBtn.SetEnabled(false)
		_ = statusLabel.SetText(connectBusy)
		go func() {
			res, err := mgr.ExchangeCode(a.ctx, pasted, ch)
			a.mw.Synchronize(func() {
				if dlg.IsDisposed() {
					return
				}
				if err != nil {
					connectBtn.SetEnabled(true)
					_ = statusLabel.SetText(fmt.Sprintf("Connect failed: %v", err))
					return
				}
				who := res.AccountEmail
				if who == "" {
					who = "your Claude account"
				}
				_ = statusLabel.SetText(connectSuccessPrefix + who)
				dlg.Accept()
				walk.MsgBox(a.mw, connectTitle, connectSuccessPrefix+who+".\r\nQuota will appear within a few seconds.", walk.MsgBoxIconInformation)
			})
			if err == nil {
				a.refreshOnce(provider.WithForceRefresh(a.ctx))
			}
		}()
	})
	cancelBtn.Clicked().Attach(func() { dlg.Cancel() })
	_ = dlg.SetDefaultButton(connectBtn)
	_ = dlg.SetCancelButton(cancelBtn)
	native.FocusTopmost(uintptr(dlg.Handle()))
	dlg.Run()
}
