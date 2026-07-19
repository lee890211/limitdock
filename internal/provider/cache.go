package provider

import (
	"context"
	"sync"
	"time"

	"limitdock/internal/readmodel"
)

type forceRefreshKey struct{}

// WithForceRefresh marks ctx so cached readers bypass their throttle window
// (manual "Updated" click, post-Connect refresh).
func WithForceRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceRefreshKey{}, true)
}

func forceRefresh(ctx context.Context) bool {
	v, _ := ctx.Value(forceRefreshKey{}).(bool)
	return v
}

// CachePolicy controls how often a wrapped reader may hit its backing source
// and how failures degrade into stale/error display states.
type CachePolicy struct {
	MinInterval   time.Duration
	BackoffLadder []time.Duration  // intervals applied on consecutive rate-limited failures; nil = MinInterval only
	IsRateLimited func(error) bool // classifies an error as rate limiting; nil = never
	SnapshotKey   string           // used with ProviderID/AccountID to synthesize an error card when no cached data exists
	ProviderID    string
	AccountID     string
	Now           func() time.Time // test hook; nil = time.Now
}

// WithCache wraps a reader so repeated aggregator polls within MinInterval
// replay the last result instead of re-fetching, a failed fetch serves the
// last good model flagged stale, and rate-limited failures back off.
func WithCache(inner Reader, policy CachePolicy) Reader {
	c := &cachedReader{inner: inner, policy: policy}
	if fb, ok := inner.(FallbackReader); ok {
		// Preserve the FallbackReader interface only for readers that
		// actually implement it.
		return &cachedFallbackReader{cachedReader: c, providerID: fb.FallbackProviderID()}
	}
	return c
}

type cachedReader struct {
	inner  Reader
	policy CachePolicy

	mu          sync.Mutex
	haveResult  bool
	lastFetch   time.Time
	lastModel   *readmodel.ReadModel
	lastErr     error
	lastGood    *readmodel.ReadModel
	backoffStep int
}

type cachedFallbackReader struct {
	*cachedReader
	providerID string
}

func (c *cachedFallbackReader) FallbackProviderID() string { return c.providerID }

func (c *cachedReader) Name() string { return c.inner.Name() }

func (c *cachedReader) Read(ctx context.Context) (*readmodel.ReadModel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.haveResult && !forceRefresh(ctx) && now.Sub(c.lastFetch) < c.currentInterval() {
		return c.lastModel, c.lastErr
	}

	model, err := c.inner.Read(ctx)
	c.haveResult = true
	c.lastFetch = now

	if err == nil {
		c.backoffStep = 0
		if hasMetrics(model) {
			// Status-only models (needs_auth/error placeholders) must not
			// become "last good": a later transient failure would then mask
			// the sign-in prompt behind a meaningless stale card.
			c.lastGood = model
		}
		c.lastModel, c.lastErr = model, nil
		return model, nil
	}

	if c.policy.IsRateLimited != nil && c.policy.IsRateLimited(err) {
		if c.backoffStep < len(c.policy.BackoffLadder) {
			c.backoffStep++
		}
	} else {
		c.backoffStep = 0
	}

	if c.lastGood != nil {
		c.lastModel, c.lastErr = staleCopy(c.lastGood, err.Error()), nil
		return c.lastModel, nil
	}
	if c.policy.SnapshotKey != "" {
		c.lastModel = statusReadModel(c.policy.SnapshotKey, c.policy.ProviderID, c.policy.AccountID, readmodel.StatusError, err.Error())
		c.lastErr = nil
		return c.lastModel, nil
	}
	c.lastModel, c.lastErr = nil, err
	return nil, err
}

func (c *cachedReader) currentInterval() time.Duration {
	if c.backoffStep > 0 && len(c.policy.BackoffLadder) > 0 {
		idx := c.backoffStep - 1
		if idx >= len(c.policy.BackoffLadder) {
			idx = len(c.policy.BackoffLadder) - 1
		}
		return c.policy.BackoffLadder[idx]
	}
	return c.policy.MinInterval
}

func (c *cachedReader) now() time.Time {
	if c.policy.Now != nil {
		return c.policy.Now()
	}
	return time.Now()
}

// hasMetrics reports whether any snapshot carries actual metric rows.
func hasMetrics(model *readmodel.ReadModel) bool {
	if model == nil {
		return false
	}
	for _, snap := range model.Snapshots {
		if snap != nil && len(snap.Metrics) > 0 {
			return true
		}
	}
	return false
}

// staleCopy clones a model shallowly, flagging every snapshot as stale so the
// UI keeps showing last-known values with a degradation marker.
func staleCopy(model *readmodel.ReadModel, reason string) *readmodel.ReadModel {
	out := &readmodel.ReadModel{Snapshots: map[string]*readmodel.Snapshot{}, Raw: model.Raw}
	for key, snap := range model.Snapshots {
		if snap == nil {
			continue
		}
		copied := *snap
		copied.Status = readmodel.StatusStale
		copied.Message = "last update failed: " + reason
		out.Snapshots[key] = &copied
	}
	return out
}
