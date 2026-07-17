package ui

import (
	"testing"

	"limitdock/internal/quota"
	"limitdock/internal/readmodel"
)

func TestProviderStatusRowsCoversAllProvidersWithDefaults(t *testing.T) {
	rows := providerStatusRows(nil)
	if len(rows) != 5 {
		t.Fatalf("expected 5 provider rows, got %d", len(rows))
	}
	for _, row := range rows {
		if row.StatusText != providerStatusNone {
			t.Fatalf("provider %s without card should be %q, got %q", row.ProviderID, providerStatusNone, row.StatusText)
		}
	}
}

func TestProviderStatusRowsMapsStatuses(t *testing.T) {
	cards := []quota.Card{
		{ProviderID: "claude_code", Status: readmodel.StatusNeedsAuth, Main: "Sign in"},
		{ProviderID: "codex", Status: readmodel.StatusStale, Main: "40%"},
		{ProviderID: "gemini_cli", Status: readmodel.StatusOK, Main: "72%"},
		{ProviderID: "cursor", Status: readmodel.StatusError, Main: "Error"},
	}
	rows := providerStatusRows(cards)
	byID := map[string]providerStatusRow{}
	for _, row := range rows {
		byID[row.ProviderID] = row
	}
	if byID["claude_code"].StatusText != providerStatusNeedsAuth {
		t.Fatalf("claude: %q", byID["claude_code"].StatusText)
	}
	if byID["codex"].StatusText != providerStatusStale {
		t.Fatalf("codex: %q", byID["codex"].StatusText)
	}
	if got := byID["gemini_cli"].StatusText; got != providerStatusOK+" — 72% left" {
		t.Fatalf("gemini: %q", got)
	}
	if byID["cursor"].StatusText != providerStatusError {
		t.Fatalf("cursor: %q", byID["cursor"].StatusText)
	}
	if byID["antigravity"].StatusText != providerStatusNone {
		t.Fatalf("antigravity: %q", byID["antigravity"].StatusText)
	}
}

func TestCardsNeedClaudeConnect(t *testing.T) {
	if cardsNeedClaudeConnect([]quota.Card{{ProviderID: "codex", Status: readmodel.StatusNeedsAuth}}) {
		t.Fatal("codex needs_auth must not trigger the Claude connect entry")
	}
	if !cardsNeedClaudeConnect([]quota.Card{{ProviderID: "claude_code", Status: readmodel.StatusNeedsAuth}}) {
		t.Fatal("claude needs_auth should trigger the connect entry")
	}
	if cardsNeedClaudeConnect([]quota.Card{{ProviderID: "claude_code", Status: readmodel.StatusOK}}) {
		t.Fatal("healthy claude must not trigger the connect entry")
	}
}
