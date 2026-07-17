package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"limitdock/internal/claudeauth"
	"limitdock/internal/readmodel"
)

type Logger interface {
	Printf(format string, args ...any)
}

type Reader interface {
	Name() string
	Read(ctx context.Context) (*readmodel.ReadModel, error)
}

type FallbackReader interface {
	FallbackProviderID() string
}

type Aggregator struct {
	Readers []Reader
	Log     Logger
}

func (a Aggregator) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	out := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{}}
	var firstErr error
	for _, reader := range a.Readers {
		if reader == nil {
			continue
		}
		model, err := reader.Read(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", reader.Name(), err)
			}
			if a.Log != nil {
				a.Log.Printf("Provider reader %s failed: %v", reader.Name(), err)
			}
			continue
		}
		if fallback, ok := reader.(FallbackReader); ok {
			model = filterFallbackModel(out, model, fallback.FallbackProviderID(), reader.Name(), a.Log)
		}
		mergeReadModel(out, model, reader.Name(), a.Log)
	}
	if len(out.Snapshots) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return orderReadModel(out), nil
}

func filterFallbackModel(existing, fallback *readmodel.ReadModel, providerID, source string, log Logger) *readmodel.ReadModel {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" || fallback == nil || len(fallback.Snapshots) == 0 || !hasProviderQuota(existing, providerID) {
		return fallback
	}
	out := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{}}
	for key, snap := range fallback.Snapshots {
		if snap == nil || strings.ToLower(strings.TrimSpace(snap.ProviderID)) == providerID {
			if log != nil && key != "__invalid" {
				log.Printf("Provider fallback %s skipped %s because %s already has quota rows", source, key, providerID)
			}
			continue
		}
		out.Snapshots[key] = snap
	}
	return out
}

func hasProviderQuota(model *readmodel.ReadModel, providerID string) bool {
	if model == nil {
		return false
	}
	for _, snap := range model.Snapshots {
		if snap == nil || strings.ToLower(strings.TrimSpace(snap.ProviderID)) != providerID {
			continue
		}
		for key, metric := range snap.Metrics {
			if isQuotaMetric(key, metric) {
				return true
			}
		}
	}
	return false
}

func isQuotaMetric(key string, metric readmodel.Metric) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "quota" || strings.HasPrefix(k, "quota_") || strings.HasPrefix(k, "rate_limit_") || strings.HasPrefix(k, "usage_seven_day") {
		return true
	}
	if k == "usage_five_hour" || k == "plan_percent_used" || strings.HasSuffix(k, "_quota") {
		return true
	}
	return metric.Unit == "%"
}

func mergeReadModel(dst, src *readmodel.ReadModel, source string, log Logger) {
	if dst == nil || src == nil || len(src.Snapshots) == 0 {
		return
	}
	if dst.Snapshots == nil {
		dst.Snapshots = map[string]*readmodel.Snapshot{}
	}
	keys := make([]string, 0, len(src.Snapshots))
	for key := range src.Snapshots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		snap := src.Snapshots[key]
		if snap == nil || key == "__invalid" {
			continue
		}
		if _, exists := dst.Snapshots[key]; exists {
			if log != nil {
				log.Printf("Provider reader %s skipped duplicate snapshot %s", source, key)
			}
			continue
		}
		dst.Snapshots[key] = snap
	}
}

func orderReadModel(model *readmodel.ReadModel) *readmodel.ReadModel {
	if model == nil || len(model.Snapshots) == 0 {
		return model
	}
	keys := make([]string, 0, len(model.Snapshots))
	for key := range model.Snapshots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := map[string]*readmodel.Snapshot{}
	for _, key := range keys {
		ordered[key] = model.Snapshots[key]
	}
	model.Snapshots = ordered
	return model
}

// statusSnapshot builds a metrics-free snapshot that carries only a provider
// status (needs_auth, stale, error) for UI surfacing.
func statusSnapshot(providerID, accountID, status, message string) *readmodel.Snapshot {
	return &readmodel.Snapshot{
		ProviderID: providerID,
		AccountID:  accountID,
		Status:     status,
		Message:    message,
		Metrics:    map[string]readmodel.Metric{},
	}
}

// statusReadModel wraps statusSnapshot under the given snapshot key.
func statusReadModel(key, providerID, accountID, status, message string) *readmodel.ReadModel {
	return &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
		key: statusSnapshot(providerID, accountID, status, message),
	}}
}

// HTTPStatusError reports a non-2xx provider API response so callers can react
// to specific status codes (e.g. 429 backoff in the cache wrapper).
type HTTPStatusError struct {
	StatusCode int
	Status     string
}

func (e *HTTPStatusError) Error() string {
	if strings.TrimSpace(e.Status) != "" {
		return "HTTP " + e.Status
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// IsRateLimitError reports whether err is an HTTP 429 response, either from a
// usage API call or from claudeauth's token endpoint.
func IsRateLimitError(err error) bool {
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 429 {
		return true
	}
	return errors.Is(err, claudeauth.ErrRateLimited)
}

func snapshotKey(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return "local"
	}
	return strings.Join(out, "-")
}
