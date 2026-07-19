package ui

import (
	"errors"
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
	connectInstructions     = "2. Approve LimitDock in the browser.\r\n3. Copy the code the page shows (looks like abc12...#xyz89...). LimitDock fills it automatically when it looks like a sign-in code; otherwise use Paste or Ctrl+V."
	connectPasteHint        = "Paste the code here"
	connectPasteButton      = "Paste"
	connectButton           = "Connect"
	connectBusy             = "Connecting..."
	connectRateLimited      = "Anthropic is rate limiting this device (HTTP 429). Wait 2-3 minutes, then click Connect again — the same code still works."
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
	a.centerDialog(dlg, 560, 360)
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

	// LineEdit (not multiline TextEdit): the dock/dialog message loop can
	// swallow Ctrl+V on some TextEdit setups, so we also wire an explicit
	// Paste button and clipboard auto-fill below.
	pasteEdit, _ := walk.NewLineEdit(dlg)
	_ = pasteEdit.SetText("")
	_ = pasteEdit.SetCueBanner(connectPasteHint)

	pasteRow, _ := walk.NewComposite(dlg)
	pasteLayout := leftButtonLayout(pasteRow)
	pasteBtn, _ := walk.NewPushButton(pasteRow)
	_ = pasteBtn.SetText(connectPasteButton)
	addButtonSpacer(pasteRow, pasteLayout)

	statusLabel, _ := walk.NewLabel(dlg)
	_ = statusLabel.SetText(connectPasteHint)

	applyClipboard := func(force bool) bool {
		text, err := walk.Clipboard().Text()
		if err != nil {
			if force {
				_ = statusLabel.SetText("Clipboard is empty or unavailable.")
			}
			return false
		}
		text = strings.TrimSpace(text)
		if text == "" {
			if force {
				_ = statusLabel.SetText("Clipboard is empty.")
			}
			return false
		}
		if !force && !looksLikeClaudeAuthCode(text) {
			return false
		}
		_ = pasteEdit.SetText(text)
		_ = statusLabel.SetText("Code ready — click Connect.")
		return true
	}

	pasteBtn.Clicked().Attach(func() { _ = applyClipboard(true) })
	pasteEdit.KeyDown().Attach(func(key walk.Key) {
		if key == walk.KeyV && walk.ModifiersDown()&walk.ModControl != 0 {
			_ = applyClipboard(true)
		}
	})
	clipboardHandler := walk.Clipboard().ContentsChanged().Attach(func() {
		if dlg.IsDisposed() {
			return
		}
		_ = applyClipboard(false)
	})
	defer walk.Clipboard().ContentsChanged().Detach(clipboardHandler)

	openBtn.Clicked().Attach(func() {
		if err := openURL(authorizeURL); err != nil {
			// Fall back to the clipboard so the user can open it manually.
			_ = walk.Clipboard().SetText(authorizeURL)
			_ = statusLabel.SetText("Could not open the browser; the sign-in URL was copied to your clipboard.")
			return
		}
		_ = statusLabel.SetText("Browser opened — approve LimitDock, then copy the code (auto-fill / Paste / Ctrl+V).")
	})

	buttons, _ := walk.NewComposite(dlg)
	bl := leftButtonLayout(buttons)
	addButtonSpacer(buttons, bl)
	connectBtn, _ := walk.NewPushButton(buttons)
	_ = connectBtn.SetText(connectButton)
	cancelBtn, _ := walk.NewPushButton(buttons)
	_ = cancelBtn.SetText(settingsCancel)

	connectBtn.Clicked().Attach(func() {
		pasted := strings.TrimSpace(pasteEdit.Text())
		if pasted == "" {
			// Last chance: if the field is empty but the clipboard already
			// holds a code, use it instead of forcing another paste step.
			if !applyClipboard(false) {
				_ = statusLabel.SetText(connectPasteHint)
				return
			}
			pasted = strings.TrimSpace(pasteEdit.Text())
		}
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
					if errors.Is(err, claudeauth.ErrRateLimited) {
						// The token endpoint rate-limits per IP; the pasted
						// code stays valid for a few minutes, so a plain
						// retry usually succeeds once the window clears.
						_ = statusLabel.SetText(connectRateLimited)
						return
					}
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
	_ = pasteEdit.SetFocus()
	_ = applyClipboard(false)
	dlg.Run()
}

// looksLikeClaudeAuthCode reports whether clipboard text is worth auto-filling
// into the Connect dialog. Prefer the CODE#STATE form; bare codes must be long
// enough that random clipboard scraps are unlikely to match.
func looksLikeClaudeAuthCode(text string) bool {
	code, _, err := claudeauth.ParsePastedCode(text)
	if err != nil {
		return false
	}
	if strings.Contains(text, "#") {
		return true
	}
	return len(code) >= 20
}
