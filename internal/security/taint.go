// Package security implements Frog Bot V2's defense-in-depth security layer:
//   - End-to-end Taint Tracking (blocking second-order persistent memory/skill/cron poisoning)
//   - Independent Three-State Reviewer Gateway (APPROVE / DENY / ASK_USER + permanent whitelist)
//   - Controlled execution bridge (`frog-exec`) over Unix domain socket
package security

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// TaintSource records one untrusted external input that tainted a turn or message chain.
type TaintSource struct {
	Kind   string    `json:"kind"`   // "email" | "web" | "peer" | "callback" | "tool"
	Origin string    `json:"origin"` // e.g. "imap:boss@example.com", "https://...", "bot@node"
	At     time.Time `json:"at"`
}

// TaintSnapshot is an immutable, serializable copy of taint provenance for cross-node/callback propagation.
type TaintSnapshot struct {
	Tainted bool          `json:"tainted"`
	Sources []TaintSource `json:"sources,omitempty"`
}

// TaintTracker maintains taint provenance for an active turn/session.
type TaintTracker struct {
	mu      sync.RWMutex
	tainted bool
	sources []TaintSource
}

func NewTaintTracker() *TaintTracker { return &TaintTracker{} }

// Mark stamps the session as tainted by an untrusted external source.
func (t *TaintTracker) Mark(kind, origin string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tainted = true
	t.sources = append(t.sources, TaintSource{Kind: kind, Origin: origin, At: time.Now()})
}

// Merge merges upstream taint (e.g. from a cross-node FrogLink peer message or TaskCallback).
func (t *TaintTracker) Merge(snap TaintSnapshot) {
	if t == nil || !snap.Tainted {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tainted = true
	t.sources = append(t.sources, snap.Sources...)
}

func (t *TaintTracker) IsTainted() bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.tainted
}

func (t *TaintTracker) Snapshot() TaintSnapshot {
	if t == nil {
		return TaintSnapshot{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TaintSnapshot{
		Tainted: t.tainted,
		Sources: slices.Clone(t.sources),
	}
}

// Reset clears taint at the boundary of a fresh user-initiated session turn.
func (t *TaintTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tainted = false
	t.sources = nil
}

// PersistentMutationActions lists memory, skill, and schedule write actions that MUST pass
// independent review whenever Tainted == true to block delayed second-order memory poisoning.
var PersistentMutationActions = map[string]bool{
	"memory_manage":   true,
	"memory_write":    true,
	"skill_save":      true,
	"create_schedule": true,
	"update_schedule": true,
	"delete_schedule": true,
	"cron_upsert":     true,
	"cron_delete":     true,
}

// OutboundActions lists actions that send external data or use secrets and always require gateway screening.
var OutboundActions = map[string]bool{
	"frog_exec":         true,
	"secret_use":        true,
	"send_email":        true,
	"send_peer_message": true,
	"message_agent":     true,
}

// RequiresGatewayReview returns true if this action must be adjudicated by the independent SecurityGateway.
func RequiresGatewayReview(action string, tainted bool, hasSecrets bool) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	if hasSecrets || OutboundActions[action] {
		return true
	}
	if tainted && PersistentMutationActions[action] {
		return true
	}
	return false
}
