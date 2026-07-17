package readmodel

import "strings"

// Snapshot.Status values shared by provider readers and the quota/UI layers.
const (
	StatusOK        = "ok"
	StatusNeedsAuth = "needs_auth" // credentials exist locally but are unusable (expired and refresh failed)
	StatusStale     = "stale"      // last-known-good data shown because the latest fetch failed
	StatusError     = "error"      // non-auth failure with no usable cached data
)

// NeedsAttention reports whether a status should keep a provider card visible
// even when the snapshot carries no quota bands.
func NeedsAttention(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case StatusNeedsAuth, StatusStale, StatusError:
		return true
	}
	return false
}
