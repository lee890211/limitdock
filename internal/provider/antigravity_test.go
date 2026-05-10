package provider

import (
	"testing"

	"limitdock/internal/settings"
)

func TestAntigravityStatusReadModelRequiresQuota(t *testing.T) {
	model := antigravityStatusReadModel(map[string]any{
		"userStatus": map[string]any{"email": "user@example.com"},
	}, settings.Defaults().Antigravity)
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
	model := antigravityStatusReadModel(status, settings.Defaults().Antigravity)
	snap := model.Snapshots["antigravity-user@example.com"]
	if snap == nil {
		t.Fatalf("expected antigravity snapshot: %#v", model.Snapshots)
	}
	if snap.ProviderID != "antigravity" || snap.Metrics["quota_prompt_credits"].Limit == nil {
		t.Fatalf("unexpected prompt quota metric: %#v", snap)
	}
	metric := snap.Metrics["quota_model_gemini_3_pro"]
	if metric.Remaining == nil || *metric.Remaining != 42 {
		t.Fatalf("expected model quota remaining percent, got %#v", metric)
	}
	if snap.Resets["quota_model_gemini_3_pro_reset"] == nil {
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
	model := antigravityStatusReadModel(status, settings.Defaults().Antigravity)
	if model.Snapshots["antigravity-user@example.com"] == nil {
		t.Fatalf("expected nested antigravity snapshot: %#v", model.Snapshots)
	}
}
