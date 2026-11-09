// Package mailtest provides a Mailer that records messages, for tests.
package mailtest

import (
	"context"
	"sync"

	"github.com/raphi011/mise/internal/mail"
)

// Recorder records sent messages. Err, when set, fails every send.
type Recorder struct {
	mu   sync.Mutex
	sent []mail.Message
	Err  error
	// notify receives a value per send, for tests waiting on background
	// sends.
	notify chan struct{}
}

// New returns an empty Recorder.
func New() *Recorder { return &Recorder{notify: make(chan struct{}, 100)} }

// Send records m, or fails with r.Err.
func (r *Recorder) Send(_ context.Context, m mail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.sent = append(r.sent, m)
	r.notify <- struct{}{}
	return nil
}

// Sent returns the messages sent so far.
func (r *Recorder) Sent() []mail.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mail.Message(nil), r.sent...)
}

// Wait blocks until n messages have been sent since the last Wait, or the
// context ends.
func (r *Recorder) Wait(ctx context.Context, n int) bool {
	for range n {
		select {
		case <-r.notify:
		case <-ctx.Done():
			return false
		}
	}
	return true
}
