// Package tap implements a pub/sub inference event tap for real-time dashboard streaming.
package tap

import (
	"sync"
)

// InferenceEvent represents a single inference lifecycle event.
type InferenceEvent struct {
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Provider  string `json:"provider"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"` // "request" | "chunk" | "complete" | "error"
	Data      string `json:"data,omitempty"`
	Model     string `json:"model,omitempty"`
	TokensIn  int    `json:"tokensIn,omitempty"`
	TokensOut int    `json:"tokensOut,omitempty"`
}

// subscriber holds a channel and an optional agentID filter.
type subscriber struct {
	ch      chan InferenceEvent
	agentID string // empty = all events
}

// InferenceTap is a fan-out pub/sub bus for inference events.
type InferenceTap struct {
	mu   sync.RWMutex
	subs map[uint64]*subscriber
	next uint64
}

// New creates a new InferenceTap.
func New() *InferenceTap {
	return &InferenceTap{
		subs: make(map[uint64]*subscriber),
	}
}

// Subscribe returns a read-only channel and an unsubscribe function.
// If agentID is empty, all events are delivered; otherwise only events matching agentID.
func (t *InferenceTap) Subscribe(agentID string) (<-chan InferenceEvent, func()) {
	ch := make(chan InferenceEvent, 64)

	t.mu.Lock()
	id := t.next
	t.next++
	t.subs[id] = &subscriber{ch: ch, agentID: agentID}
	t.mu.Unlock()

	unsub := func() {
		t.mu.Lock()
		delete(t.subs, id)
		t.mu.Unlock()
		// Drain to unblock any pending Publish
		for len(ch) > 0 {
			<-ch
		}
	}

	return ch, unsub
}

// Publish sends an event to all matching subscribers.
// It is non-blocking: events are dropped if a subscriber's channel is full.
func (t *InferenceTap) Publish(event InferenceEvent) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for _, sub := range t.subs {
		if sub.agentID != "" && sub.agentID != event.AgentID {
			continue
		}
		// Non-blocking send: drop if full
		select {
		case sub.ch <- event:
		default:
		}
	}
}
