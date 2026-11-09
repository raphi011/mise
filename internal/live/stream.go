package live

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/starfederation/datastar-go/datastar"
)

// DefaultHeartbeat is how often an idle stream sends a comment, so proxies
// and browsers keep the connection open.
const DefaultHeartbeat = 25 * time.Second

// Stream describes one page's live region.
type Stream struct {
	// Refresh renders the region for the viewer and sends it as element
	// patches. It runs when the stream opens (which also covers a client
	// reopening it after the tab was hidden) and after relevant events.
	Refresh func(ctx context.Context, sse *datastar.ServerSentEventGenerator) error
	// Relevant reports whether an event affects the region.
	Relevant func(Event) bool
	// Alive reports whether the viewer's session is still valid. It runs
	// on every heartbeat and when the viewer's sessions change; the stream
	// ends when it reports false.
	Alive func(ctx context.Context) (bool, error)
	// Heartbeat overrides DefaultHeartbeat.
	Heartbeat time.Duration
}

// Serve streams sub's events to the client until the request ends, the
// subscription ends (hub shutdown, or a newer stream of the same session),
// the session ends, or a write fails. It clears the server's read and write
// deadlines for this connection only.
func Serve(w http.ResponseWriter, r *http.Request, sub *Subscription, s Stream) error {
	ctx := r.Context()
	rc := http.NewResponseController(w)
	// The server's ReadTimeout also bounds the connection's background
	// read, which cancels the request when it expires; clear both.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if err := rc.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	// nginx buffers responses unless told otherwise.
	w.Header().Set("X-Accel-Buffering", "no")
	sse := datastar.NewSSE(w, r)

	if err := s.Refresh(ctx, sse); err != nil {
		return err
	}

	beat := s.Heartbeat
	if beat <= 0 {
		beat = DefaultHeartbeat
	}
	ticker := time.NewTicker(beat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Done():
			return nil
		case <-ticker.C:
			if ok, err := s.Alive(ctx); err != nil || !ok {
				return err
			}
			// A comment line keeps the connection open without an event.
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return nil // the client went away
			}
			if err := rc.Flush(); err != nil {
				return nil // the client went away
			}
		case <-sub.Ready():
			events, overflow := sub.Drain()
			refresh, check := overflow, false
			for _, e := range events {
				if e.Kind == SessionsChanged && e.UserID == sub.UserID() {
					check = true
				}
				if s.Relevant(e) {
					refresh = true
				}
			}
			if check {
				if ok, err := s.Alive(ctx); err != nil || !ok {
					return err
				}
			}
			if refresh {
				if err := s.Refresh(ctx, sse); err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
			}
		}
	}
}
