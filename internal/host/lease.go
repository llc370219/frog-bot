package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Standard resource key builders for V2 mutual exclusion leases.
func WorkspaceWriteResource(botID string) string { return "workspace_write:" + botID }
func BrowserProfileResource(botID string) string { return "browser_profile:" + botID }

var ErrLeaseNotHeld = errors.New("lease not held or already expired")

// LeaseInfo describes an active lease for observability and diagnostics.
type LeaseInfo struct {
	ID         string    `json:"id"`
	Resource   string    `json:"resource"`
	Holder     string    `json:"holder"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Lease is an active exclusive lock handle with a TTL.
type Lease struct {
	mgr      *LeaseManager
	id       string
	resource string
	holder   string
}

func (l *Lease) ID() string       { return l.id }
func (l *Lease) Resource() string { return l.resource }
func (l *Lease) Holder() string   { return l.holder }

// Renew extends the TTL of this lease if still held.
func (l *Lease) Renew(ttl time.Duration) error {
	if l == nil || l.mgr == nil {
		return ErrLeaseNotHeld
	}
	return l.mgr.Renew(l.resource, l.id, ttl)
}

// Release releases this lease immediately and wakes any queued waiters.
func (l *Lease) Release() {
	if l == nil || l.mgr == nil {
		return
	}
	l.mgr.Release(l.resource, l.id)
}

type leaseEntry struct {
	info    LeaseInfo
	changed chan struct{} // closed whenever released or replaced
}

// LeaseManager maintains TTL-bounded in-memory mutual exclusion locks
// (e.g. workspace_write:<bot> and browser_profile:<bot>) to eliminate file corruption
// and Chromium SingletonLock deadlocks across concurrent CLI / cron / browser tasks.
type LeaseManager struct {
	Now func() time.Time

	mu     sync.Mutex
	leases map[string]*leaseEntry
}

func NewLeaseManager() *LeaseManager {
	return &LeaseManager{
		leases: make(map[string]*leaseEntry),
	}
}

func (m *LeaseManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func newLeaseID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "lease_" + hex.EncodeToString(b[:])
}

// TryAcquire attempts to acquire an exclusive lease without blocking.
// Returns (lease, true) if acquired, or (nil, false) if currently held by another live holder.
func (m *LeaseManager) TryAcquire(resource, holder string, ttl time.Duration) (*Lease, bool) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if cur, ok := m.leases[resource]; ok {
		if now.Before(cur.info.ExpiresAt) {
			return nil, false
		}
		close(cur.changed)
		delete(m.leases, resource)
	}
	id := newLeaseID()
	m.leases[resource] = &leaseEntry{
		info: LeaseInfo{
			ID:         id,
			Resource:   resource,
			Holder:     holder,
			AcquiredAt: now,
			ExpiresAt:  now.Add(ttl),
		},
		changed: make(chan struct{}),
	}
	return &Lease{mgr: m, id: id, resource: resource, holder: holder}, true
}

// Acquire blocks until the resource lease is obtained, an existing lease expires,
// or ctx is canceled.
func (m *LeaseManager) Acquire(ctx context.Context, resource, holder string, ttl time.Duration) (*Lease, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	for {
		m.mu.Lock()
		now := m.now()
		cur, ok := m.leases[resource]
		if !ok || !now.Before(cur.info.ExpiresAt) {
			if ok {
				close(cur.changed)
				delete(m.leases, resource)
			}
			id := newLeaseID()
			m.leases[resource] = &leaseEntry{
				info: LeaseInfo{
					ID:         id,
					Resource:   resource,
					Holder:     holder,
					AcquiredAt: now,
					ExpiresAt:  now.Add(ttl),
				},
				changed: make(chan struct{}),
			}
			m.mu.Unlock()
			return &Lease{mgr: m, id: id, resource: resource, holder: holder}, nil
		}
		waitCh := cur.changed
		waitDur := cur.info.ExpiresAt.Sub(now)
		m.mu.Unlock()

		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("acquire lease %s: %w", resource, ctx.Err())
		case <-waitCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Renew extends a live lease by ttl from now.
func (m *LeaseManager) Renew(resource, leaseID string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	cur, ok := m.leases[resource]
	if !ok || cur.info.ID != leaseID || !now.Before(cur.info.ExpiresAt) {
		return ErrLeaseNotHeld
	}
	cur.info.ExpiresAt = now.Add(ttl)
	return nil
}

// Release releases the lease matching resource+leaseID.
func (m *LeaseManager) Release(resource, leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.leases[resource]
	if !ok || cur.info.ID != leaseID {
		return
	}
	delete(m.leases, resource)
	close(cur.changed)
}

// IsHeld reports whether resource currently has an unexpired lease.
func (m *LeaseManager) IsHeld(resource string) (LeaseInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	cur, ok := m.leases[resource]
	if !ok || !now.Before(cur.info.ExpiresAt) {
		return LeaseInfo{}, false
	}
	return cur.info, true
}

// Active returns all currently live leases sorted by resource key.
func (m *LeaseManager) Active() []LeaseInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []LeaseInfo
	for k, cur := range m.leases {
		if !now.Before(cur.info.ExpiresAt) {
			close(cur.changed)
			delete(m.leases, k)
			continue
		}
		out = append(out, cur.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}
