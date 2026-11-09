// Package live fans out change events to the pages household members have
// open. Writes publish a typed event after their transaction commits; each
// open page holds one Datastar SSE stream (see Serve) that re-renders its
// region for the viewer when a relevant event arrives.
//
// The hub is in-process: mise runs as a single instance (docs/plan.md, Live
// updates).
package live

import (
	"slices"
	"sync"

	"github.com/google/uuid"
)

// Kind names an event type.
type Kind string

// Event kinds.
const (
	// ListChanged: entries of the list ListID were added, changed, checked
	// off, removed or restored.
	ListChanged Kind = "list.changed"
	// ListsChanged: shopping lists were created, renamed, reordered, deleted
	// or restored.
	ListsChanged Kind = "lists.changed"
	// CatalogChanged: items or categories changed, which affects how every
	// list renders.
	CatalogChanged Kind = "catalog.changed"
	// RecipeChanged: the recipe RecipeID was edited, deleted or restored, or
	// its favourites or cook history changed.
	RecipeChanged Kind = "recipe.changed"
	// RecipesChanged: recipes were added, deleted or restored, or tags
	// changed, which affects the library and its filters.
	RecipesChanged Kind = "recipes.changed"
	// SessionsChanged: sessions of UserID ended (sign-out, revoke, password
	// change, removal). Streams of that user re-check their session.
	SessionsChanged Kind = "sessions.changed"
)

// Event is one change.
type Event struct {
	Kind     Kind
	ListID   uuid.UUID
	RecipeID uuid.UUID
	UserID   uuid.UUID
}

// Publisher accepts events after a write has committed.
type Publisher interface {
	// Publish delivers events to every subscription of the household.
	Publish(householdID uuid.UUID, events ...Event)
}

// MaxPerSession bounds the open subscriptions of one session (one browser,
// any number of tabs). Each costs a connection and a session check per
// heartbeat; a new one beyond the limit ends the oldest.
const MaxPerSession = 8

// Hub is an in-process Publisher with subscriptions per household.
type Hub struct {
	mu     sync.Mutex
	subs   map[uuid.UUID]map[*Subscription]struct{}
	seq    uint64
	closed bool
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[uuid.UUID]map[*Subscription]struct{}{}}
}

// Subscribe registers a subscription for the household's events, held by the
// given user and session. Close it when done. After Close of the hub, the
// subscription is returned already done.
func (h *Hub) Subscribe(householdID, userID uuid.UUID, sessionID string) *Subscription {
	s := &Subscription{
		hub: h, household: householdID, user: userID, session: sessionID,
		ready: make(chan struct{}, 1), done: make(chan struct{}),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.end()
		return s
	}
	h.seq++
	s.seq = h.seq
	set := h.subs[householdID]
	if set == nil {
		set = map[*Subscription]struct{}{}
		h.subs[householdID] = set
	}
	h.limitSession(set, sessionID)
	set[s] = struct{}{}
	return s
}

// limitSession ends the session's oldest subscriptions in set until there is
// room for one more. A session belongs to one household, so set holds all of
// its subscriptions. The caller holds h.mu.
func (h *Hub) limitSession(set map[*Subscription]struct{}, sessionID string) {
	for {
		var oldest *Subscription
		n := 0
		for s := range set {
			if s.session != sessionID {
				continue
			}
			n++
			if oldest == nil || s.seq < oldest.seq {
				oldest = s
			}
		}
		if n < MaxPerSession {
			return
		}
		oldest.end()
		delete(set, oldest)
	}
}

// Close ends every subscription and refuses new ones, so open streams return
// and graceful shutdown does not wait for them. Register it with
// http.Server.RegisterOnShutdown.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, set := range h.subs {
		for s := range set {
			s.end()
		}
	}
	clear(h.subs)
}

// Publish implements Publisher. It never blocks: events queue on each
// subscription until its stream drains them.
func (h *Hub) Publish(householdID uuid.UUID, events ...Event) {
	if len(events) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[householdID] {
		s.push(events)
	}
}

// PublishUser delivers e to every subscription held by userID, in any
// household. Sessions and passwords are account-wide, so session events go
// to all of the user's streams.
func (h *Hub) PublishUser(userID uuid.UUID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.subs {
		for s := range set {
			if s.user == userID {
				s.push([]Event{e})
			}
		}
	}
}

// Subscribers returns the number of open subscriptions of a household, for
// tests and diagnostics.
func (h *Hub) Subscribers(householdID uuid.UUID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[householdID])
}

func (h *Hub) remove(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set, ok := h.subs[s.household]
	if !ok {
		return
	}
	delete(set, s)
	if len(set) == 0 {
		delete(h.subs, s.household)
	}
}

// maxPending bounds the queue of a subscription whose stream has stalled.
// Streams re-render whole regions, so dropped events are replaced by a
// single overflow marker that forces a full refresh.
const maxPending = 256

// Subscription receives one household's events. Events are coalesced: a
// stream that falls behind re-renders once for the whole batch.
type Subscription struct {
	hub       *Hub
	household uuid.UUID
	user      uuid.UUID
	session   string
	seq       uint64
	ready     chan struct{}
	// done is closed when the subscription ends: Close, the hub's Close, or
	// eviction by a newer subscription of the same session.
	done chan struct{}

	mu       sync.Mutex
	pending  []Event
	overflow bool
	closed   bool
}

// UserID returns the user holding the subscription.
func (s *Subscription) UserID() uuid.UUID { return s.user }

func (s *Subscription) push(events []Event) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	for _, e := range events {
		if slices.Contains(s.pending, e) {
			continue
		}
		if len(s.pending) >= maxPending {
			s.overflow = true
			break
		}
		s.pending = append(s.pending, e)
	}
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Done is closed when the subscription has ended and its stream must stop.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// end marks the subscription closed and signals Done. It does not touch the
// hub's map.
func (s *Subscription) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.done)
}

// Ready is signalled when events are waiting; call Drain to take them.
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// Drain returns and clears the waiting events. overflow reports that events
// were dropped, so the caller must refresh everything.
func (s *Subscription) Drain() (events []Event, overflow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events, overflow = s.pending, s.overflow
	s.pending, s.overflow = nil, false
	return events, overflow
}

// Close unregisters the subscription. It is safe to call more than once.
func (s *Subscription) Close() {
	s.end()
	s.hub.remove(s)
}
