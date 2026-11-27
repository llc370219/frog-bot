package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// EventType enumerates the 4 canonical event categories consumed by a Bot Actor Inbox.
type EventType string

const (
	EventUserMessage    EventType = "user_message"
	EventTaskCallback   EventType = "task_callback"
	EventPeerMessage    EventType = "peer_message"
	EventApprovalResult EventType = "approval_result"
)

// ActorEvent is a single unit of work delivered to a Bot Actor's sequential inbox.
type ActorEvent struct {
	ID           string    `json:"id"`
	Type         EventType `json:"type"`
	BotID        string    `json:"bot_id"`
	Source       string    `json:"source,omitempty"`        // e.g. "user", "cli:task_1", "email:foo@bar.com", "bot@node"
	Text         string    `json:"text,omitempty"`
	Payload      any       `json:"payload,omitempty"`
	Tainted      bool      `json:"tainted,omitempty"`       // untrusted external input taint flag
	TaintSources []string  `json:"taint_sources,omitempty"` // provenance chain
	CreatedAt    time.Time `json:"created_at"`
}

// ActorStats reports live mailbox queue depth and processed event counts.
type ActorStats struct {
	BotID       string    `json:"bot_id"`
	Queued      int       `json:"queued"`
	Processed   int64     `json:"processed"`
	Dropped     int64     `json:"dropped"`
	Busy        bool      `json:"busy"`
	LastEventAt time.Time `json:"last_event_at,omitempty"`
}

// EventHandler processes a single event sequentially inside the Bot Actor goroutine.
type EventHandler func(ctx context.Context, ev ActorEvent) error

// BotActor encapsulates a single-goroutine sequential event mailbox for one Bot.
// UserMessage and ApprovalResult events are prioritized ahead of background
// TaskCallback / PeerMessage events whenever queued concurrently, while never
// allowing two turns to mutate chat state concurrently.
type BotActor struct {
	botID   string
	handler EventHandler
	maxQ    int

	mu          sync.Mutex
	highQ       []ActorEvent // UserMessage, ApprovalResult
	normalQ     []ActorEvent // TaskCallback, PeerMessage
	notify      chan struct{}
	busy        bool
	closed      bool
	processed   int64
	dropped     int64
	lastEventAt time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewBotActor(botID string, maxQueue int, handler EventHandler) *BotActor {
	if maxQueue <= 0 {
		maxQueue = 256
	}
	return &BotActor{
		botID:   botID,
		handler: handler,
		maxQ:    maxQueue,
		notify:  make(chan struct{}, 1),
	}
}

// Start launches the single-goroutine sequential event loop.
func (a *BotActor) Start(parent context.Context) {
	a.mu.Lock()
	if a.ctx != nil {
		a.mu.Unlock()
		return
	}
	a.ctx, a.cancel = context.WithCancel(parent)
	a.mu.Unlock()

	a.wg.Add(1)
	go a.loop()
}

// Close stops the actor gracefully and waits for the active event to finish.
func (a *BotActor) Close() {
	a.mu.Lock()
	a.closed = true
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.wg.Wait()
}

// Submit enqueues an event into the Bot Actor's mailbox. Non-blocking.
func (a *BotActor) Submit(ev ActorEvent) error {
	if ev.ID == "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		ev.ID = "ev_" + hex.EncodeToString(b[:])
	}
	if ev.BotID == "" {
		ev.BotID = a.botID
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("bot actor closed")
	}
	if len(a.highQ)+len(a.normalQ) >= a.maxQ {
		a.dropped++
		return errors.New("bot actor inbox full")
	}
	switch ev.Type {
	case EventUserMessage, EventApprovalResult:
		a.highQ = append(a.highQ, ev)
	default:
		a.normalQ = append(a.normalQ, ev)
	}
	select {
	case a.notify <- struct{}{}:
	default:
	}
	return nil
}

func (a *BotActor) pop() (ActorEvent, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.highQ) > 0 {
		ev := a.highQ[0]
		a.highQ = a.highQ[1:]
		a.busy = true
		return ev, true
	}
	if len(a.normalQ) > 0 {
		ev := a.normalQ[0]
		a.normalQ = a.normalQ[1:]
		a.busy = true
		return ev, true
	}
	a.busy = false
	return ActorEvent{}, false
}

func (a *BotActor) loop() {
	defer a.wg.Done()
	for {
		for {
			if a.ctx.Err() != nil {
				return
			}
			ev, ok := a.pop()
			if !ok {
				break
			}
			if a.handler != nil {
				_ = a.handler(a.ctx, ev)
			}
			a.mu.Lock()
			a.processed++
			a.lastEventAt = time.Now()
			a.busy = false
			a.mu.Unlock()
		}
		select {
		case <-a.ctx.Done():
			return
		case <-a.notify:
		}
	}
}

func (a *BotActor) Stats() ActorStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ActorStats{
		BotID:       a.botID,
		Queued:      len(a.highQ) + len(a.normalQ),
		Processed:   a.processed,
		Dropped:     a.dropped,
		Busy:        a.busy,
		LastEventAt: a.lastEventAt,
	}
}

// StatelessTaskResult is the ephemeral outcome of a history-free background task.
// Only if ReportToActor is true is a condensed summary submitted back to the Bot Actor.
type StatelessTaskResult struct {
	ReportToActor bool
	Summary       string
	Payload       any
	Tainted       bool
	TaintSources  []string
}

// RunStatelessTask executes an isolated, zero-history background check (e.g. email triage,
// scheduled check, long-text summarization) concurrently outside the main Bot Actor mailbox,
// enforcing budget / rate limits and posting a condensed TaskCallback only when warranted.
func RunStatelessTask(
	ctx context.Context,
	actor *BotActor,
	budget *BudgetGuard,
	source string,
	fn func(ctx context.Context) (StatelessTaskResult, error),
) error {
	if budget != nil {
		if ok, reason := budget.AllowBackgroundEvent(source); !ok {
			return errors.New(reason)
		}
	}
	res, err := fn(ctx)
	if err != nil {
		return err
	}
	if res.ReportToActor && actor != nil {
		return actor.Submit(ActorEvent{
			Type:         EventTaskCallback,
			Source:       source,
			Text:         res.Summary,
			Payload:      res.Payload,
			Tainted:      res.Tainted,
			TaintSources: res.TaintSources,
		})
	}
	return nil
}
