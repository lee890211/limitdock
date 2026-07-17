package claudeauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type PKCEChallenge struct {
	Verifier  string
	Challenge string
	State     string
}

// NewPKCEChallenge creates the verifier/challenge/state trio for one
// authorization attempt.
func NewPKCEChallenge() (PKCEChallenge, error) {
	verifier, err := randomToken()
	if err != nil {
		return PKCEChallenge{}, err
	}
	state, err := randomToken()
	if err != nil {
		return PKCEChallenge{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return PKCEChallenge{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
		State:     state,
	}, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// BuildAuthorizeURL returns the browser URL where the user approves LimitDock
// and receives a CODE#STATE string to paste back.
func (m Manager) BuildAuthorizeURL(ch PKCEChallenge) string {
	q := url.Values{
		"code":                  {"true"},
		"client_id":             {ClaudeOAuthClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {oauthScopes},
		"code_challenge":        {ch.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {ch.State},
	}
	return authorizeURL + "?" + q.Encode()
}

// ParsePastedCode splits the "CODE#STATE" string shown on the approval page.
// A missing state part is tolerated (the code alone is still exchangeable).
func ParsePastedCode(pasted string) (code, state string, err error) {
	s := strings.TrimSpace(pasted)
	if s == "" {
		return "", "", fmt.Errorf("paste the code shown after approving in the browser")
	}
	if i := strings.LastIndex(s, "#"); i >= 0 {
		code, state = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	} else {
		code = s
	}
	if code == "" {
		return "", "", fmt.Errorf("the pasted value has no authorization code before the #")
	}
	return code, state, nil
}

type ExchangeResult struct {
	AccountEmail string
	Scopes       string
	ExpiresAt    time.Time
}

// ExchangeCode swaps a pasted authorization code for tokens and persists them
// into LimitDock's own credential store.
func (m Manager) ExchangeCode(ctx context.Context, pasted string, ch PKCEChallenge) (ExchangeResult, error) {
	code, state, err := ParsePastedCode(pasted)
	if err != nil {
		return ExchangeResult{}, err
	}
	if state != "" && state != ch.State {
		return ExchangeResult{}, fmt.Errorf("the pasted code belongs to a different sign-in attempt; close the dialog and try Connect again")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"state":         {ch.State},
		"redirect_uri":  {redirectURI},
		"client_id":     {ClaudeOAuthClientID},
		"code_verifier": {ch.Verifier},
	}
	res, err := m.postTokenForm(ctx, form)
	if err != nil {
		return ExchangeResult{}, err
	}
	st := storedToken{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresAt:    m.now().Add(time.Duration(res.ExpiresIn) * time.Second).UnixMilli(),
		AccountEmail: res.Account.EmailAddress,
		Scopes:       res.Scope,
	}
	if err := m.store().Save(storeTokenName, st); err != nil {
		return ExchangeResult{}, fmt.Errorf("connected, but saving the token failed: %w", err)
	}
	return ExchangeResult{
		AccountEmail: st.AccountEmail,
		Scopes:       st.Scopes,
		ExpiresAt:    time.UnixMilli(st.ExpiresAt),
	}, nil
}
