package auth

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// requestStatsFlushInterval controls how often dirty request counters are flushed.
//
// Request counters change on every request, so they are flushed on a timer
// instead of synchronously like cooldown state.
const requestStatsFlushInterval = 15 * time.Second

// exportRecentRequests returns the non-empty buckets held by the ring buffer.
func (a *Auth) exportRecentRequests() []RequestStatsBucketRecord {
	if a == nil {
		return nil
	}
	records := make([]RequestStatsBucketRecord, 0, recentRequestBucketCount)
	for i := range a.recentRequests.buckets {
		bucket := a.recentRequests.buckets[i]
		if bucket.bucketID == 0 || (bucket.success == 0 && bucket.failed == 0) {
			continue
		}
		records = append(records, RequestStatsBucketRecord{
			BucketID: bucket.bucketID,
			Success:  bucket.success,
			Failed:   bucket.failed,
		})
	}
	return records
}

// importRecentRequests restores ring buffer buckets, dropping entries that have
// already scrolled out of the retention window.
func (a *Auth) importRecentRequests(records []RequestStatsBucketRecord, now time.Time) {
	if a == nil {
		return
	}
	oldest := recentRequestBucketID(now) - int64(recentRequestBucketCount-1)
	for i := range a.recentRequests.buckets {
		a.recentRequests.buckets[i] = recentRequestBucket{}
	}
	for _, record := range records {
		if record.BucketID < oldest {
			continue
		}
		idx := recentRequestBucketIndex(record.BucketID)
		a.recentRequests.buckets[idx] = recentRequestBucket{
			bucketID: record.BucketID,
			success:  record.Success,
			failed:   record.Failed,
		}
	}
}

// requestStatsRuntime owns the flush loop state for request statistics.
type requestStatsRuntime struct {
	mu     sync.Mutex
	store  RequestStatsStore
	dirty  atomic.Bool
	cancel context.CancelFunc
}

// SetRequestStatsStore installs the request statistics store and starts the
// background flush loop. Passing nil disables persistence.
func (m *Manager) SetRequestStatsStore(store RequestStatsStore) {
	if m == nil {
		return
	}
	m.requestStats.mu.Lock()
	defer m.requestStats.mu.Unlock()

	if m.requestStats.cancel != nil {
		m.requestStats.cancel()
		m.requestStats.cancel = nil
	}
	m.requestStats.store = store
	if store == nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.requestStats.cancel = cancel
	go m.runRequestStatsFlushLoop(ctx)
}

// markRequestStatsDirty flags that counters changed since the last flush.
func (m *Manager) markRequestStatsDirty() {
	if m == nil {
		return
	}
	m.requestStats.dirty.Store(true)
}

func (m *Manager) requestStatsStore() RequestStatsStore {
	if m == nil {
		return nil
	}
	m.requestStats.mu.Lock()
	defer m.requestStats.mu.Unlock()
	return m.requestStats.store
}

func (m *Manager) runRequestStatsFlushLoop(ctx context.Context) {
	ticker := time.NewTicker(requestStatsFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !m.requestStats.dirty.Swap(false) {
				continue
			}
			if errFlush := m.PersistRequestStats(ctx); errFlush != nil {
				log.Warnf("failed to persist request statistics: %v", errFlush)
			}
		}
	}
}

// PersistRequestStats writes the current counters for every registered auth.
func (m *Manager) PersistRequestStats(ctx context.Context) error {
	if m == nil {
		return nil
	}
	store := m.requestStatsStore()
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return store.Save(ctx, m.requestStatsRecords())
}

// requestStatsRecords snapshots counters and health state for every auth that
// holds traffic or an active health condition worth preserving.
func (m *Manager) requestStatsRecords() []RequestStatsRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()

	records := make([]RequestStatsRecord, 0, len(m.auths))
	now := time.Now().UTC()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		buckets := auth.exportRecentRequests()
		if auth.Success == 0 && auth.Failed == 0 && len(buckets) == 0 && !auth.hasHealthState() {
			continue
		}
		records = append(records, RequestStatsRecord{
			Provider:       auth.Provider,
			AuthID:         auth.ID,
			AuthFile:       auth.FileName,
			Success:        auth.Success,
			Failed:         auth.Failed,
			Buckets:        buckets,
			UpdatedAt:      now,
			Status:         auth.Status,
			StatusMessage:  auth.StatusMessage,
			Unavailable:    auth.Unavailable,
			NextRetryAfter: auth.NextRetryAfter,
			Quota:          auth.Quota,
			LastError:      cloneError(auth.LastError),
			ModelStates:    cloneModelStates(auth.ModelStates),
		})
	}
	return records
}

// hasHealthState reports whether an auth carries any non-default health fields
// that would be lost across a restart.
func (a *Auth) hasHealthState() bool {
	if a == nil {
		return false
	}
	if a.Status != "" && a.Status != StatusActive {
		return true
	}
	if strings.TrimSpace(a.StatusMessage) != "" {
		return true
	}
	if a.Unavailable {
		return true
	}
	if !a.NextRetryAfter.IsZero() {
		return true
	}
	if a.Quota.Exceeded || a.Quota.Reason != "" || !a.Quota.NextRecoverAt.IsZero() || a.Quota.BackoffLevel != 0 {
		return true
	}
	if a.LastError != nil {
		return true
	}
	for _, state := range a.ModelStates {
		if state == nil {
			continue
		}
		if state.Status != StatusActive || state.Unavailable || strings.TrimSpace(state.StatusMessage) != "" ||
			!state.NextRetryAfter.IsZero() || state.LastError != nil ||
			state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
			return true
		}
	}
	return false
}

func cloneModelStates(states map[string]*ModelState) map[string]*ModelState {
	if len(states) == 0 {
		return nil
	}
	cloned := make(map[string]*ModelState, len(states))
	for model, state := range states {
		cloned[model] = state.Clone()
	}
	return cloned
}

// ResetRequestStats clears counters for every auth and drops persisted state.
func (m *Manager) ResetRequestStats(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		auth.Success = 0
		auth.Failed = 0
		auth.importRecentRequests(nil, time.Now())
	}
	m.mu.Unlock()

	m.requestStats.dirty.Store(false)
	store := m.requestStatsStore()
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return store.Save(ctx, nil)
}

// RestoreRequestStats loads persisted counters and health state back into
// registered auths.
func (m *Manager) RestoreRequestStats(ctx context.Context) error {
	if m == nil {
		return nil
	}
	store := m.requestStatsStore()
	if store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	records, errLoad := store.Load(ctx)
	if errLoad != nil {
		return errLoad
	}
	if len(records) == 0 {
		return nil
	}

	now := time.Now()
	snapshotsByID := make(map[string]*Auth)
	m.mu.Lock()
	for _, record := range records {
		auth, ok := m.auths[record.AuthID]
		if !ok || auth == nil {
			continue
		}
		auth.Success = record.Success
		auth.Failed = record.Failed
		auth.importRecentRequests(record.Buckets, now)
		// Cooldown state follows the same gate as cooldown persistence: when
		// cooling is disabled for an auth, do not resurrect stale health windows.
		if !m.cooldownDisabledForAuth(auth) && !auth.Disabled && auth.Status != StatusDisabled {
			restoreRequestStatsHealth(auth, record, now)
		}
		snapshotsByID[auth.ID] = auth.Clone()
	}
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, snapshot := range snapshotsByID {
			m.scheduler.upsertAuth(snapshot)
		}
	}
	return nil
}

// restoreRequestStatsHealth merges a persisted health snapshot into an auth,
// only applying fields that are still relevant (unexpired retry windows).
func restoreRequestStatsHealth(auth *Auth, record RequestStatsRecord, now time.Time) {
	if auth == nil {
		return
	}
	status := record.Status
	statusMessage := strings.TrimSpace(record.StatusMessage)
	unavailable := record.Unavailable
	nextRetryAfter := record.NextRetryAfter
	quota := record.Quota
	if quota.Exceeded && quota.NextRecoverAt.IsZero() && !nextRetryAfter.IsZero() {
		quota.NextRecoverAt = nextRetryAfter
	}

	// Re-apply model states so aggregated availability can be recomputed below.
	if len(record.ModelStates) > 0 {
		if auth.ModelStates == nil {
			auth.ModelStates = make(map[string]*ModelState)
		}
		for model, state := range record.ModelStates {
			if state == nil {
				continue
			}
			auth.ModelStates[model] = state.Clone()
		}
	}

	if nextRetryAfter.IsZero() || !nextRetryAfter.After(now) {
		// The cooldown window already closed; do not resurrect stale health.
		updateAggregatedAvailability(auth, now)
		return
	}

	if unavailable {
		auth.Unavailable = true
		auth.Status = StatusError
		auth.NextRetryAfter = nextRetryAfter
		auth.Quota = quota
		auth.UpdatedAt = now
		if statusMessage != "" {
			auth.StatusMessage = statusMessage
		} else if status != "" && status != StatusActive {
			auth.StatusMessage = string(status)
		}
		if record.LastError != nil {
			auth.LastError = cloneError(record.LastError)
		}
	}
	updateAggregatedAvailability(auth, now)
}
