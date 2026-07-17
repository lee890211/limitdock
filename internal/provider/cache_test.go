package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"limitdock/internal/claudeauth"
	"limitdock/internal/readmodel"
)

func TestIsRateLimitErrorClassifies(t *testing.T) {
	if !IsRateLimitError(&HTTPStatusError{StatusCode: 429}) {
		t.Fatal("HTTP 429 should be rate limited")
	}
	if !IsRateLimitError(fmt.Errorf("claude auth: %w", claudeauth.ErrRateLimited)) {
		t.Fatal("wrapped claudeauth.ErrRateLimited should be rate limited")
	}
	if IsRateLimitError(&HTTPStatusError{StatusCode: 500}) {
		t.Fatal("HTTP 500 is not rate limiting")
	}
	if IsRateLimitError(errors.New("boom")) {
		t.Fatal("generic error is not rate limiting")
	}
}

// countingReader is a controllable inner reader for cache tests.
type countingReader struct {
	calls  int
	model  *readmodel.ReadModel
	err    error
	failAt int // fail on call numbers >= failAt (1-based); 0 = never fail
}

func (r *countingReader) Name() string { return "counting" }

func (r *countingReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	r.calls++
	if r.failAt > 0 && r.calls >= r.failAt {
		return nil, r.err
	}
	return r.model, nil
}

type countingFallbackReader struct {
	countingReader
}

func (r *countingFallbackReader) FallbackProviderID() string { return "counting" }

func quotaModel(key string) *readmodel.ReadModel {
	used := 40.0
	return &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{
		key: {
			ProviderID: "counting",
			AccountID:  key,
			Status:     readmodel.StatusOK,
			Metrics: map[string]readmodel.Metric{
				"usage_five_hour": {Used: &used, Unit: "%", Window: "5h"},
			},
		},
	}}
}

func newFakeClock(start time.Time) (*time.Time, func() time.Time) {
	now := start
	return &now, func() time.Time { return now }
}

func TestCacheThrottlesWithinMinInterval(t *testing.T) {
	now, clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := &countingReader{model: quotaModel("counting-a")}
	r := WithCache(inner, CachePolicy{MinInterval: time.Minute, Now: clock})

	for i := 0; i < 3; i++ {
		if _, err := r.Read(context.Background()); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 inner call within interval, got %d", inner.calls)
	}

	*now = now.Add(61 * time.Second)
	if _, err := r.Read(context.Background()); err != nil {
		t.Fatalf("read after interval: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected second inner call after interval, got %d", inner.calls)
	}
}

func TestCacheForceRefreshBypassesThrottle(t *testing.T) {
	_, clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := &countingReader{model: quotaModel("counting-a")}
	r := WithCache(inner, CachePolicy{MinInterval: time.Hour, Now: clock})

	r.Read(context.Background())
	r.Read(WithForceRefresh(context.Background()))
	if inner.calls != 2 {
		t.Fatalf("force refresh should bypass throttle, got %d calls", inner.calls)
	}
}

func TestCacheServesStaleAfterFailure(t *testing.T) {
	now, clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := &countingReader{model: quotaModel("counting-a"), err: errors.New("boom"), failAt: 2}
	r := WithCache(inner, CachePolicy{MinInterval: time.Minute, Now: clock})

	if _, err := r.Read(context.Background()); err != nil {
		t.Fatalf("first read: %v", err)
	}
	*now = now.Add(2 * time.Minute)
	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("failure with cache should not error: %v", err)
	}
	snap := model.Snapshots["counting-a"]
	if snap == nil || snap.Status != readmodel.StatusStale {
		t.Fatalf("expected stale snapshot, got %#v", snap)
	}
	if len(snap.Metrics) == 0 {
		t.Fatalf("stale snapshot should keep last-known metrics")
	}
	if snap.Message == "" {
		t.Fatalf("stale snapshot should carry the failure reason")
	}
}

func TestCacheSynthesizesErrorCardWithoutCache(t *testing.T) {
	_, clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := &countingReader{err: errors.New("boom"), failAt: 1}
	r := WithCache(inner, CachePolicy{
		MinInterval: time.Minute,
		SnapshotKey: "counting-a",
		ProviderID:  "counting",
		AccountID:   "counting-a",
		Now:         clock,
	})

	model, err := r.Read(context.Background())
	if err != nil {
		t.Fatalf("error-card path should not error: %v", err)
	}
	snap := model.Snapshots["counting-a"]
	if snap == nil || snap.Status != readmodel.StatusError {
		t.Fatalf("expected error snapshot, got %#v", snap)
	}
}

func TestCachePropagatesErrorWithoutCacheOrKey(t *testing.T) {
	_, clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := &countingReader{err: errors.New("boom"), failAt: 1}
	r := WithCache(inner, CachePolicy{MinInterval: time.Minute, Now: clock})
	if _, err := r.Read(context.Background()); err == nil {
		t.Fatal("expected propagated error")
	}
}

func TestCacheBackoffLadderOnRateLimit(t *testing.T) {
	now, clock := newFakeClock(time.Unix(1_700_000_000, 0))
	rateErr := &HTTPStatusError{StatusCode: 429, Status: "429 Too Many Requests"}
	inner := &countingReader{model: quotaModel("counting-a"), err: rateErr, failAt: 2}
	r := WithCache(inner, CachePolicy{
		MinInterval:   time.Minute,
		BackoffLadder: []time.Duration{3 * time.Minute, 6 * time.Minute},
		IsRateLimited: func(err error) bool {
			var httpErr *HTTPStatusError
			return errors.As(err, &httpErr) && httpErr.StatusCode == 429
		},
		Now: clock,
	})

	r.Read(context.Background()) // success (call 1)
	*now = now.Add(2 * time.Minute)
	r.Read(context.Background()) // failure → backoff step 1 (call 2)
	if inner.calls != 2 {
		t.Fatalf("setup: expected 2 calls, got %d", inner.calls)
	}

	*now = now.Add(2 * time.Minute) // within 3m backoff → replay
	r.Read(context.Background())
	if inner.calls != 2 {
		t.Fatalf("read within backoff must replay, got %d calls", inner.calls)
	}

	*now = now.Add(90 * time.Second) // past 3m → refetch, fails again → step 2
	r.Read(context.Background())
	if inner.calls != 3 {
		t.Fatalf("expected refetch after first backoff, got %d calls", inner.calls)
	}

	*now = now.Add(5 * time.Minute) // within 6m backoff → replay
	r.Read(context.Background())
	if inner.calls != 3 {
		t.Fatalf("read within second backoff must replay, got %d calls", inner.calls)
	}

	*now = now.Add(2 * time.Minute) // past 6m → refetch; inner recovers
	inner.failAt = 0
	r.Read(context.Background())
	if inner.calls != 4 {
		t.Fatalf("expected refetch after second backoff, got %d calls", inner.calls)
	}

	// Success resets the ladder to MinInterval.
	*now = now.Add(61 * time.Second)
	r.Read(context.Background())
	if inner.calls != 5 {
		t.Fatalf("expected MinInterval cadence after recovery, got %d calls", inner.calls)
	}
}

func TestCachePreservesFallbackInterfaceOnlyWhenPresent(t *testing.T) {
	plain := WithCache(&countingReader{model: quotaModel("a")}, CachePolicy{MinInterval: time.Minute})
	if _, ok := plain.(FallbackReader); ok {
		t.Fatal("plain reader wrapper must not gain FallbackReader")
	}
	fb := WithCache(&countingFallbackReader{countingReader{model: quotaModel("a")}}, CachePolicy{MinInterval: time.Minute})
	got, ok := fb.(FallbackReader)
	if !ok || got.FallbackProviderID() != "counting" {
		t.Fatalf("fallback wrapper must preserve FallbackReader, got %v %v", got, ok)
	}
}

func TestCacheDoesNotMutateLastGoodOnStale(t *testing.T) {
	now, clock := newFakeClock(time.Unix(1_700_000_000, 0))
	inner := &countingReader{model: quotaModel("counting-a"), err: errors.New("boom"), failAt: 2}
	r := WithCache(inner, CachePolicy{MinInterval: time.Minute, Now: clock})

	r.Read(context.Background())
	*now = now.Add(2 * time.Minute)
	r.Read(context.Background()) // stale copy

	if inner.model.Snapshots["counting-a"].Status != readmodel.StatusOK {
		t.Fatalf("stale copy must not mutate the cached good model")
	}
}
