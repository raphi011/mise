package live

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/starfederation/datastar-go/datastar"
)

func TestHub(t *testing.T) {
	h := NewHub()
	a, b := uuid.New(), uuid.New()
	ua, ub := uuid.New(), uuid.New()
	subA := h.Subscribe(a, ua, "sa")
	subB := h.Subscribe(b, ub, "sb")
	defer subB.Close()

	list := uuid.New()
	h.Publish(a, Event{Kind: ListChanged, ListID: list}, Event{Kind: ListChanged, ListID: list}, Event{Kind: ListsChanged})
	select {
	case <-subA.Ready():
	default:
		t.Fatal("subscriber not signalled")
	}
	events, overflow := subA.Drain()
	if overflow || len(events) != 2 {
		t.Errorf("events not coalesced: %v, %v", events, overflow)
	}
	select {
	case <-subB.Ready():
		t.Error("other household signalled")
	default:
	}

	h.PublishUser(ub, Event{Kind: SessionsChanged, UserID: ub})
	if events, _ := subB.Drain(); len(events) != 1 || events[0].UserID != ub {
		t.Errorf("user event: %v", events)
	}
	if events, _ := subA.Drain(); len(events) != 0 {
		t.Errorf("user event reached another user: %v", events)
	}

	for range maxPending + 10 {
		h.Publish(a, Event{Kind: ListChanged, ListID: uuid.New()})
	}
	if events, overflow := subA.Drain(); !overflow || len(events) != maxPending {
		t.Errorf("overflow: %d events, %v", len(events), overflow)
	}

	subA.Close()
	subA.Close()
	if h.Subscribers(a) != 0 {
		t.Error("closed subscription still registered")
	}
	h.Publish(a, Event{Kind: ListsChanged})
}

func TestHubLimitsSession(t *testing.T) {
	h := NewHub()
	hh, user := uuid.New(), uuid.New()
	var subs []*Subscription
	for range MaxPerSession {
		subs = append(subs, h.Subscribe(hh, user, "phone"))
	}
	other := h.Subscribe(hh, user, "laptop")
	newest := h.Subscribe(hh, user, "phone")
	select {
	case <-subs[0].Done():
	default:
		t.Fatal("oldest subscription of the session not ended")
	}
	for _, s := range append(subs[1:], other, newest) {
		select {
		case <-s.Done():
			t.Fatal("subscription ended although within the limit")
		default:
		}
	}
	if n := h.Subscribers(hh); n != MaxPerSession+1 {
		t.Errorf("%d subscribers, want %d", n, MaxPerSession+1)
	}
	subs[0].Close() // the stream's deferred Close after eviction
	if n := h.Subscribers(hh); n != MaxPerSession+1 {
		t.Errorf("closing an evicted subscription removed another: %d", n)
	}
}

func TestHubClose(t *testing.T) {
	h := NewHub()
	hh := uuid.New()
	sub := h.Subscribe(hh, uuid.New(), "s")
	h.Close()
	select {
	case <-sub.Done():
	default:
		t.Fatal("subscription not ended by hub Close")
	}
	late := h.Subscribe(hh, uuid.New(), "s")
	select {
	case <-late.Done():
	default:
		t.Fatal("subscription after hub Close not ended")
	}
	if n := h.Subscribers(hh); n != 0 {
		t.Errorf("%d subscribers after Close", n)
	}
	sub.Close()
	late.Close()
	h.Publish(hh, Event{Kind: ListsChanged})
}

func TestServe(t *testing.T) {
	h := NewHub()
	hh, user := uuid.New(), uuid.New()
	var refreshes, alive atomic.Int32
	alive.Store(1)
	list := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := h.Subscribe(hh, user, "s")
		defer sub.Close()
		err := Serve(w, r, sub, Stream{
			Refresh: func(_ context.Context, sse *datastar.ServerSentEventGenerator) error {
				n := refreshes.Add(1)
				return sse.PatchElementf(`<div id="region">%d</div>`, n)
			},
			Relevant:  func(e Event) bool { return e.Kind == ListChanged && e.ListID == list },
			Alive:     func(context.Context) (bool, error) { return alive.Load() == 1, nil },
			Heartbeat: 50 * time.Millisecond,
		})
		if err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers: %v", res.Header)
	}
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitFor := func(want string) {
		t.Helper()
		timeout := time.After(2 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended waiting for %q", want)
				}
				if strings.Contains(l, want) {
					return
				}
			case <-timeout:
				t.Fatalf("timed out waiting for %q", want)
			}
		}
	}
	waitFor(`<div id="region">1</div>`)
	for h.Subscribers(hh) == 0 {
		time.Sleep(time.Millisecond)
	}
	h.Publish(hh, Event{Kind: ListChanged, ListID: uuid.New()}) // another list: ignored
	h.Publish(hh, Event{Kind: ListChanged, ListID: list})
	waitFor(`<div id="region">2</div>`)
	waitFor(": ping")

	// Ending the subscription (hub shutdown, eviction) ends the stream.
	h.Close()
	for range lines {
	}
	if n := refreshes.Load(); n != 2 {
		t.Errorf("%d refreshes, want 2", n)
	}
}

func TestServeEndsWithSession(t *testing.T) {
	h := NewHub()
	hh, user := uuid.New(), uuid.New()
	var alive atomic.Int32
	alive.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := h.Subscribe(hh, user, "s")
		defer sub.Close()
		err := Serve(w, r, sub, Stream{
			Refresh: func(_ context.Context, sse *datastar.ServerSentEventGenerator) error {
				return sse.PatchElementf(`<div id="region"></div>`)
			},
			Relevant:  func(Event) bool { return false },
			Alive:     func(context.Context) (bool, error) { return alive.Load() == 1, nil },
			Heartbeat: time.Hour,
		})
		if err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	ended := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
		}
		close(ended)
	}()
	for h.Subscribers(hh) == 0 {
		time.Sleep(time.Millisecond)
	}

	// A revoked session ends the stream.
	alive.Store(0)
	h.PublishUser(user, Event{Kind: SessionsChanged, UserID: user})
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("stream still open after the session ended")
	}
}
