package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAntigravityStatusReadModelRequiresQuota(t *testing.T) {
	model := antigravityStatusReadModel(map[string]any{
		"userStatus": map[string]any{"email": "user@example.com"},
	})
	if len(model.Snapshots) != 0 {
		t.Fatalf("expected no visible snapshot without quota metrics: %#v", model.Snapshots)
	}
}

func TestAntigravityStatusReadModelBuildsQuotaSnapshot(t *testing.T) {
	status := map[string]any{
		"response": map[string]any{
			"userStatus": map[string]any{
				"email":                  "user@example.com",
				"availablePromptCredits": float64(25),
				"monthlyPromptCredits":   float64(100),
			},
			"cascadeModelConfigData": map[string]any{
				"clientModelConfigs": []any{
					map[string]any{
						"modelOrAlias": map[string]any{"model": "gemini-3-pro"},
						"quotaInfo": map[string]any{
							"remainingFraction": float64(0.42),
							"resetTime":         "2026-05-11T00:00:00Z",
						},
					},
				},
			},
		},
	}
	model := antigravityStatusReadModel(status)
	snap := model.Snapshots["antigravity-user@example.com"]
	if snap == nil {
		t.Fatalf("expected antigravity snapshot: %#v", model.Snapshots)
	}
	if snap.ProviderID != "antigravity" || snap.Metrics["quota_prompt_credits"].Limit == nil {
		t.Fatalf("unexpected prompt quota metric: %#v", snap)
	}
	metric := snap.Metrics["quota_model_gemini_pro"]
	if metric.Remaining == nil || *metric.Remaining != 42 {
		t.Fatalf("expected model quota remaining percent, got %#v", metric)
	}
	if snap.Resets["quota_model_gemini_pro_reset"] == nil {
		t.Fatalf("expected model reset: %#v", snap.Resets)
	}
}

func TestAntigravityStatusReadModelFindsNestedQuota(t *testing.T) {
	status := map[string]any{
		"cached": map[string]any{
			"deep": []any{
				map[string]any{
					"user": map[string]any{"email": "user@example.com"},
					"clientModelConfigs": []any{
						map[string]any{
							"model": "gemini-3-pro",
							"quotaInfo": map[string]any{
								"remainingFraction": float64(0.5),
							},
						},
					},
				},
			},
		},
	}
	model := antigravityStatusReadModel(status)
	if model.Snapshots["antigravity-user@example.com"] == nil {
		t.Fatalf("expected nested antigravity snapshot: %#v", model.Snapshots)
	}
}

func TestAntigravityStatusReadModelPoolsModelQuotas(t *testing.T) {
	status := map[string]any{
		"clientModelConfigs": []any{
			map[string]any{
				"label": "Gemini 3 Pro (High)",
				"quotaInfo": map[string]any{
					"remainingFraction": float64(0.7),
				},
			},
			map[string]any{
				"label": "Gemini 3 Pro (Low)",
				"quotaInfo": map[string]any{
					"remainingFraction": float64(0.2),
				},
			},
			map[string]any{
				"label": "Claude Sonnet 4.5",
				"quotaInfo": map[string]any{
					"remainingFraction": float64(0.4),
				},
			},
		},
	}
	model := antigravityStatusReadModel(status)
	snap := model.Snapshots["antigravity-local"]
	if snap == nil {
		t.Fatalf("expected antigravity snapshot: %#v", model.Snapshots)
	}
	if got := *snap.Metrics["quota_model_gemini_pro"].Remaining; got != 20 {
		t.Fatalf("expected lowest Gemini Pro remaining percent, got %v", got)
	}
	if snap.Metrics["quota_model_claude"].Remaining == nil {
		t.Fatalf("expected Claude pooled row: %#v", snap.Metrics)
	}
}

func TestFetchAntigravityStatusFallsBackToCommandModelConfigs(t *testing.T) {
	var gotCSRF string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCSRF = r.Header.Get("X-Codeium-Csrf-Token")
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetUnleashData"):
			http.Error(w, "validation", http.StatusBadRequest)
		case strings.HasSuffix(r.URL.Path, "/GetUserStatus"):
			http.Error(w, "missing", http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/GetCommandModelConfigs"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"clientModelConfigs":[{"label":"Gemini 3 Pro","quotaInfo":{"remainingFraction":0.33}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	status, err := fetchAntigravityStatus(context.Background(), antigravityEndpoint{BaseURL: server.URL, Token: "csrf-token"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotCSRF != "csrf-token" {
		t.Fatalf("missing csrf header: %q", gotCSRF)
	}
	if len(antigravityModelConfigs(status)) != 1 {
		t.Fatalf("expected command model configs: %#v", status)
	}
}
